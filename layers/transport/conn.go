package transport

import (
	"context"
	"net"

	"github.com/google/gopacket"
)

type (
	conn interface {
		net.Conn
		protocolHandshake(ctx context.Context) error
		recv(segment gopacket.TransportLayer)
	}
)
