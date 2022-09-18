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
		handshake() error
		recv(segment gopacket.TransportLayer)
	}
)
