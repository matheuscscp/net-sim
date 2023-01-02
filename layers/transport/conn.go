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
		// setHandshakeContext must be used to inform the connection
		// about the context under which the handshake should run so
		// it can block on this context, waiting for the handshake
		// to finish before reading or writing bytes.
		setHandshakeContext(ctx context.Context)
		// doHandshake must be called after a non-nil doHandshake context
		// has been set with setHandshakeContext().
		doHandshake() error
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

func (cc *clientConn) Close() error {
	var l *listener
	switch c := cc.Conn.(type) {
	case *tcpConn:
		l = c.l
	case *udpConn:
		l = c.l
	}
	return pkgio.Close(cc.Conn, l)
}
