package transport

import (
	"context"
	"net"

	"github.com/google/gopacket"
)

type (
	conn interface {
		net.Conn
		setHandshakeContext(ctx context.Context)
		// handshake must be called after a non-nil handshake context
		// has been set with setHandshakeContext().
		handshake() error
		recv(segment gopacket.TransportLayer)
	}
)
