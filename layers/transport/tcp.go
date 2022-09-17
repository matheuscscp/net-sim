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
	}
)

func (*tcp) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return DeserializeTCPSegment(datagram)
}

func (*tcp) newConn(l *listener, remoteAddr addr) conn {
	return &tcpConn{
		l:          l,
		remoteAddr: remoteAddr,
	}
}

func (t *tcpConn) handshakeDial(ctx context.Context) error {
	return nil // TODO
}

func (t *tcpConn) handshakeAccept(ctx context.Context) error {
	return nil // TODO
}

func (t *tcpConn) recv(segment gopacket.TransportLayer) {
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
