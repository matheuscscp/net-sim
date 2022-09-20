package transport

import (
	"context"
	"net"

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
		// handshake must be called after a non-nil handshake context
		// has been set with setHandshakeContext().
		handshake() error
		recv(segment gopacket.TransportLayer)
	}
)
