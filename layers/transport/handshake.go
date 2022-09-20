package transport

import (
	"context"

	"github.com/google/gopacket"
)

type (
	handshake interface {
		recv(segment gopacket.TransportLayer)
		do(ctx context.Context, c conn) error
	}
)
