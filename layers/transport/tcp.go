package transport

import (
	"context"
	"fmt"
	"net"
	"time"

	pkgcontext "github.com/matheuscscp/net-sim/pkg/context"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/hashicorp/go-multierror"
)

type (
	tcp struct{}

	tcpConn struct {
		ctx           context.Context
		cancelCtx     context.CancelFunc
		l             *listener
		remoteAddr    addr
		h             handshake
		hCtx          context.Context
		readDeadline  *deadline
		writeDeadline *deadline

		seq, ack uint32
	}
)

func (tcp) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return DeserializeTCPSegment(datagram)
}

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

	// TODO
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

	return 0, nil // TODO
}

func (t *tcpConn) Write(b []byte) (n int, err error) {
	if err := t.waitHandshake(); err != nil {
		return 0, err
	}

	return 0, nil // TODO
}

func (t *tcpConn) Close() error {
	// cancel ctx
	var cancel context.CancelFunc
	cancel, t.cancelCtx = t.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()

	t.readDeadline.close()
	t.writeDeadline.close()

	return nil
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
