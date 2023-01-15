package transport

import (
	"context"
	"net"

	pkgio "github.com/matheuscscp/net-sim/pkg/io"

	"github.com/google/gopacket"
)

type (
	conn interface {
		net.Conn
		doHandshake(ctx context.Context) error
		recv(segment gopacket.TransportLayer)
	}

	// clientConn represents a client connection. It wraps a conn for
	// overriding the Close() method in order to close the listener
	// that was created solely for the purpose of creating the wrapped
	// conn.
	clientConn struct {
		net.Conn
	}
)

func (c *clientConn) Close() error {
	var listener *listener
	switch conn := c.Conn.(type) {
	case *tcpConn:
		listener = conn.listener
	case *udpConn:
		listener = conn.listener
	}
	return pkgio.Close(c.Conn, listener)
}
