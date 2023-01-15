package transport

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/matheuscscp/net-sim/layers/common"
	pkgcontext "github.com/matheuscscp/net-sim/pkg/context"
	pkgio "github.com/matheuscscp/net-sim/pkg/io"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/hashicorp/go-multierror"
)

type (
	udpConn struct {
		ctx           context.Context
		cancelCtx     context.CancelFunc
		listener      *listener
		remoteAddr    addr
		in            chan []byte
		readDeadline  *deadline
		writeDeadline *deadline
	}
)

func (udpFactory) newConn(listener *listener, remoteAddr addr, _ handshake) conn {
	ctx, cancel := context.WithCancel(context.Background())
	return &udpConn{
		ctx:           ctx,
		cancelCtx:     cancel,
		listener:      listener,
		remoteAddr:    remoteAddr,
		in:            make(chan []byte, channelSize),
		readDeadline:  newDeadline(),
		writeDeadline: newDeadline(),
	}
}

func (u *udpConn) doHandshake(ctx context.Context) error {
	return nil // no-op
}

func (u *udpConn) recv(segment gopacket.TransportLayer) {
	select {
	case u.in <- segment.LayerPayload():
	default:
	}
}

// Read blocks waiting for one UDP segment. b should have enough space for
// the whole UDP segment payload (any exceeding bytes will be discarded).
func (u *udpConn) Read(b []byte) (n int, err error) {
	// create deadline context
	var deadlineExceeded bool
	ctx, cancel := u.readDeadline.withContext(u.ctx, &deadlineExceeded)
	defer cancel()
	if deadlineExceeded {
		return 0, ErrDeadlineExceeded
	}
	if ctx.Err() != nil {
		return 0, fmt.Errorf("(*udpConn).ctx done before reading bytes: %w", ctx.Err())
	}

	// read
	select {
	case payload := <-u.in:
		return copy(b, payload), nil
	case <-ctx.Done():
		if deadlineExceeded {
			return 0, ErrDeadlineExceeded
		}
		return 0, fmt.Errorf("(*udpConn).ctx done while waiting for udp segment: %w", ctx.Err())
	}
}

func (u *udpConn) Write(b []byte) (n int, err error) {
	// validate payload size
	if len(b) == 0 {
		return 0, common.ErrCannotSendEmpty
	}
	if len(b) > UDPMTU {
		return 0, fmt.Errorf("payload is larger than transport layer UDP MTU (%d)", UDPMTU)
	}

	// craft headers
	datagramHeader := &gplayers.IPv4{
		DstIP:    u.remoteAddr.ipAddress.Raw(),
		Protocol: gplayers.IPProtocolUDP,
	}
	if u.listener.ipAddress != nil {
		datagramHeader.SrcIP = u.listener.ipAddress.Raw()
	}
	segment := &gplayers.UDP{
		BaseLayer: gplayers.BaseLayer{
			Payload: b,
		},
		SrcPort: gplayers.UDPPort(u.listener.port),
		DstPort: gplayers.UDPPort(u.remoteAddr.port),
		Length:  uint16(len(b) + UDPHeaderLength),
	}

	// create deadline context
	var deadlineExceeded bool
	ctx, cancel := u.writeDeadline.withContext(u.ctx, &deadlineExceeded)
	defer cancel()
	if deadlineExceeded {
		return 0, ErrDeadlineExceeded
	}
	if ctx.Err() != nil {
		return 0, fmt.Errorf("(*udpConn).ctx done before writing bytes: %w", ctx.Err())
	}

	// write
	if err := u.listener.protocol.layer.send(ctx, datagramHeader, segment); err != nil {
		if deadlineExceeded {
			return 0, ErrDeadlineExceeded
		}
		if pkgcontext.IsContextError(ctx, err) {
			return 0, fmt.Errorf("(*udpConn).ctx done while sending udp segment: %w", err)
		}
		return 0, fmt.Errorf("error sending udp segment: %w", err)
	}

	return len(b), nil
}

func (u *udpConn) Close() error {
	// cancel ctx
	var cancel context.CancelFunc
	cancel, u.cancelCtx = u.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()

	// remove conn from listener so arriving segments are
	// not directed to this conn anymore
	u.listener.deleteConn(u.remoteAddr)

	// close deadlines
	return pkgio.Close(u.readDeadline, u.writeDeadline)
}

func (u *udpConn) LocalAddr() net.Addr {
	return u.listener.Addr()
}

func (u *udpConn) RemoteAddr() net.Addr {
	return newUDPAddr(u.remoteAddr)
}

func (u *udpConn) SetDeadline(d time.Time) error {
	var err error
	if dErr := u.SetReadDeadline(d); dErr != nil {
		err = multierror.Append(err, fmt.Errorf("error setting read deadline: %w", err))
	}
	if dErr := u.SetWriteDeadline(d); dErr != nil {
		err = multierror.Append(err, fmt.Errorf("error setting write deadline: %w", err))
	}
	return err
}

func (u *udpConn) SetReadDeadline(d time.Time) error {
	u.readDeadline.set(d)
	return nil
}

func (u *udpConn) SetWriteDeadline(d time.Time) error {
	u.writeDeadline.set(d)
	return nil
}
