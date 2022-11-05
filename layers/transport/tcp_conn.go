package transport

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/matheuscscp/net-sim/layers/common"
	pkgcontext "github.com/matheuscscp/net-sim/pkg/context"
	pkgio "github.com/matheuscscp/net-sim/pkg/io"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/hashicorp/go-multierror"
	"github.com/sirupsen/logrus"
)

type (
	tcpConn struct {
		ctx           context.Context
		cancelCtx     context.CancelFunc
		l             *listener
		remoteAddr    addr
		h             handshake
		hCtx          context.Context
		readDeadline  *deadline
		writeDeadline *deadline
		recvMu        sync.Mutex
		readMu        sync.Mutex
		writeMu       sync.Mutex

		readCh  chan []byte
		readBuf []byte

		seq, ack uint32
	}
)

func (tcp) newConn(l *listener, remoteAddr addr, h handshake) conn {
	ctx, cancel := context.WithCancel(context.Background())
	return &tcpConn{
		ctx:           ctx,
		cancelCtx:     cancel,
		l:             l,
		remoteAddr:    remoteAddr,
		h:             h,
		readDeadline:  newDeadline(),
		writeDeadline: newDeadline(),
		readCh:        make(chan []byte, channelSize),
	}
}

func (t *tcpConn) setHandshakeContext(ctx context.Context) {
	t.hCtx = ctx
}

// handshake must be called after a non-nil handshake context
// has been set with setHandshakeContext().
func (t *tcpConn) handshake() error {
	if handshake := t.h; handshake != nil {
		defer func() { t.h = nil }()

		ctx, cancel := pkgcontext.WithCancelOnAnotherContext(t.hCtx, t.ctx)
		defer cancel()
		if err := handshake.do(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

func (t *tcpConn) waitHandshake() error {
	select {
	case <-t.hCtx.Done():
		return nil
	case <-t.ctx.Done():
		return t.ctx.Err()
	}
}

func (t *tcpConn) recv(segment gopacket.TransportLayer) {
	// forward to handshake first
	if handshake := t.h; handshake != nil {
		handshake.recv(segment)
		return
	}

	s := segment.(*gplayers.TCP)
	if s.ACK {
		// TODO
	} else {
		t.recvMu.Lock()
		defer t.recvMu.Unlock()

		if s.Seq != t.ack {
			return
		}
		ack := t.ack + uint32(len(s.Payload))

		select {
		case t.readCh <- s.Payload:
			// FIXME(pimenta, #68): inefficient use of the network sending one ack
			// for every segment
			ackDatagramHeader, ackSegment := t.newDatagramHeaderAndSegment()
			ackSegment.ACK = true
			ackSegment.Ack = ack
			if err := t.l.s.transportLayer.send(t.ctx, ackDatagramHeader, ackSegment); err != nil {
				logrus.
					WithError(err).
					WithField("local_addr", t.l.Addr()).
					WithField("remote_addr", t.remoteAddr).
					WithField("segment_seq", s.Seq).
					WithField("ack", ack).
					Error("error sending tcp ack")
				return
			}
			t.ack = ack
		default:
		}
	}
}

func (t *tcpConn) newDatagramHeaderAndSegment() (*gplayers.IPv4, *gplayers.TCP) {
	datagramHeader := &gplayers.IPv4{
		DstIP:    t.remoteAddr.ipAddress.Raw(),
		Protocol: gplayers.IPProtocolTCP,
	}
	if t.l.ipAddress != nil {
		datagramHeader.SrcIP = t.l.ipAddress.Raw()
	}
	segment := &gplayers.TCP{
		DstPort: gplayers.TCPPort(t.remoteAddr.port),
		SrcPort: gplayers.TCPPort(t.l.port),
	}
	return datagramHeader, segment
}

func (t *tcpConn) Read(b []byte) (n int, err error) {
	if err := t.waitHandshake(); err != nil {
		return 0, err
	}

	// create deadline context
	ctx, cancel, deadlineExceeded := t.readDeadline.newContext(t.ctx)
	defer cancel()
	if *deadlineExceeded {
		return 0, ErrDeadlineExceeded
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}

	t.readMu.Lock()
	defer t.readMu.Unlock()

	// if some bytes are already available, return them right away
	if len(t.readBuf) > 0 {
		n := copy(b, t.readBuf)
		t.readBuf = t.readBuf[n:]
		if len(t.readBuf) == 0 {
			t.readBuf = nil
		}
		return n, nil
	}

	// no bytes available, block waiting for some
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case t.readBuf = <-t.readCh:
		n := copy(b, t.readBuf)
		t.readBuf = t.readBuf[n:]
		if len(t.readBuf) == 0 {
			t.readBuf = nil
		}
		return n, nil
	}
}

func (t *tcpConn) Write(b []byte) (n int, err error) {
	if err := t.waitHandshake(); err != nil {
		return 0, err
	}

	// validate payload size
	if len(b) == 0 {
		return 0, common.ErrCannotSendEmpty
	}

	// create deadline context
	ctx, cancel, deadlineExceeded := t.writeDeadline.newContext(t.ctx)
	defer cancel()
	if *deadlineExceeded {
		return 0, ErrDeadlineExceeded
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}

	// split in segments
	nSegments := (len(b) + TCPMTU - 1) / TCPMTU
	segments := make([]*gplayers.TCP, nSegments)
	datagramHeaders := make([]*gplayers.IPv4, nSegments)
	lastSegment := nSegments - 1
	for i := 0; i < lastSegment; i++ {
		datagramHeaders[i], segments[i] = t.newDatagramHeaderAndSegment()
		segments[i].Payload = b[i*TCPMTU : (i+1)*TCPMTU]
	}
	datagramHeaders[lastSegment], segments[lastSegment] = t.newDatagramHeaderAndSegment()
	segments[lastSegment].Payload = b[lastSegment*TCPMTU:]

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	// send segments
	for i, s := range segments {
		s.Seq = t.seq
		t.seq += uint32(len(s.Payload))

		if err := t.l.s.transportLayer.send(ctx, datagramHeaders[i], s); err != nil {
			if *deadlineExceeded {
				return 0, ErrDeadlineExceeded
			}
			return 0, fmt.Errorf("error sending tcp segment: %w", err)
		}
	}

	return len(b), nil
}

func (t *tcpConn) Close() error {
	// cancel ctx
	var cancel context.CancelFunc
	cancel, t.cancelCtx = t.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()

	return pkgio.Close(t.readDeadline, t.writeDeadline)
}

func (t *tcpConn) LocalAddr() net.Addr {
	return t.l.Addr()
}

func (t *tcpConn) RemoteAddr() net.Addr {
	return newTCPAddr(t.remoteAddr)
}

func (t *tcpConn) SetDeadline(d time.Time) error {
	var err error
	if dErr := t.SetReadDeadline(d); dErr != nil {
		err = multierror.Append(err, fmt.Errorf("error setting read deadline: %w", err))
	}
	if dErr := t.SetWriteDeadline(d); dErr != nil {
		err = multierror.Append(err, fmt.Errorf("error setting write deadline: %w", err))
	}
	return err
}

func (t *tcpConn) SetReadDeadline(d time.Time) error {
	t.readDeadline.set(d)
	return nil
}

func (t *tcpConn) SetWriteDeadline(d time.Time) error {
	t.writeDeadline.set(d)
	return nil
}
