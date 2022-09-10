package transport

import (
	"context"
	"net"
	"time"

	"github.com/matheuscscp/net-sim/layers/network"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

type (
	// TCPListener implements net.Listener for TCP.
	TCPListener struct {
	}

	// TCPConn implements net.Conn for TCP.
	TCPConn struct {
	}

	tcp struct{}
)

func newTCP(ctx context.Context, networkLayer network.Layer) *listenerSet {
	return newListenerSet(ctx, networkLayer, &tcp{})
}

func (*tcp) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return nil, nil // TODO
}

func (*tcp) newConn(l *listener, remoteAddr addr) conn {
	return nil // TODO
}

func (t *TCPListener) Accept() (net.Conn, error) {
	return nil, nil // TODO
}

func (t *TCPListener) Close() error {
	return nil // TODO
}

func (t *TCPListener) Addr() net.Addr {
	return nil // TODO
}

func (t *TCPConn) Read(b []byte) (n int, err error) {
	return 0, nil // TODO
}

func (t *TCPConn) Write(b []byte) (n int, err error) {
	return 0, nil // TODO
}

func (t *TCPConn) Close() error {
	return nil // TODO
}

func (t *TCPConn) LocalAddr() net.Addr {
	return nil // TODO
}

func (t *TCPConn) RemoteAddr() net.Addr {
	return nil // TODO
}

func (t *TCPConn) SetDeadline(d time.Time) error {
	return nil // TODO
}

func (t *TCPConn) SetReadDeadline(d time.Time) error {
	return nil // TODO
}

func (t *TCPConn) SetWriteDeadline(d time.Time) error {
	return nil // TODO
}
