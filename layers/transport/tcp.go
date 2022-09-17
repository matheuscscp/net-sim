package transport

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/hashicorp/go-multierror"
)

type (
	tcp struct{}

	tcpConn struct {
		l          *listener
		remoteAddr addr
		h          handshake
	}

	tcpClientHandshake struct{}
	tcpServerHandshake struct{}
)

func (tcp) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return DeserializeTCPSegment(datagram)
}

func (tcp) newClientHandshake() handshake {
	return &tcpClientHandshake{}
}

func (t *tcpClientHandshake) recv(segment gopacket.TransportLayer) bool {
	return true // TODO
}

func (t *tcpClientHandshake) do(ctx context.Context, c conn) error {
	return nil // TODO
}

func (tcp) newServerHandshake() handshake {
	return &tcpServerHandshake{}
}

func (t *tcpServerHandshake) recv(segment gopacket.TransportLayer) bool {
	return true // TODO
}

func (t *tcpServerHandshake) do(ctx context.Context, c conn) error {
	return nil // TODO
}

func (tcp) newConn(l *listener, remoteAddr addr, h handshake) conn {
	return &tcpConn{
		l:          l,
		remoteAddr: remoteAddr,
		h:          h,
	}
}

func (t *tcpConn) handshake(ctx context.Context) error {
	return t.h.do(ctx, t)
}

func (t *tcpConn) recv(segment gopacket.TransportLayer) {
	if t.h.recv(segment) { // forward to handshake first
		return
	}
	// TODO
}

func (t *tcpConn) Read(b []byte) (n int, err error) {
	return 0, nil // TODO
}

func (t *tcpConn) Write(b []byte) (n int, err error) {
	return 0, nil // TODO
}

func (t *tcpConn) Close() error {
	return nil // TODO
}

func (t *tcpConn) LocalAddr() net.Addr {
	return t.l.Addr()
}

func (t *tcpConn) RemoteAddr() net.Addr {
	a := t.remoteAddr
	return &a
}

// SetDeadline is the same as calling SetReadDeadline() and
// SetWriteDeadline().
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
	return nil // TODO
}

func (t *tcpConn) SetWriteDeadline(d time.Time) error {
	return nil // TODO
}
