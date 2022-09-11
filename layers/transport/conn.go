package transport

import (
	"context"
	"net"

	"github.com/google/gopacket"
)

type (
	conn interface {
		net.Conn
		handshakeDial(ctx context.Context) error
		handshakeAccept(ctx context.Context) error
		recv(segment gopacket.TransportLayer)
	}
)
