package transport

import (
	"context"
	"net"
	"time"

	"github.com/matheuscscp/net-sim/layers/network"

	gplayers "github.com/google/gopacket/layers"
)

type (
	// TCPListener implements net.Listener for TCP.
	TCPListener struct {
	}

	// TCPConn implements net.Conn for TCP.
	TCPConn struct {
	}

	tcp struct {
		ctx          context.Context
		networkLayer network.Layer

		listeners map[gplayers.TCPPort]*TCPListener
	}
)

func newTCP(ctx context.Context, networkLayer network.Layer) *tcp {
	return &tcp{
		ctx:          ctx,
		networkLayer: networkLayer,

		listeners: make(map[gplayers.TCPPort]*TCPListener),
	}
}

func (t *tcp) listen(address string) (*TCPListener, error) {
	return nil, nil // TODO
}

func (t *tcp) dial(ctx context.Context, address string) (*TCPConn, error) {
	return nil, nil // TODO
}

func (t *tcp) decapAndDemux(datagram *gplayers.IPv4) {
	// TODO
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
