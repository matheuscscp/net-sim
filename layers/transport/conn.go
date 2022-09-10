package transport

import (
	"context"
	"net"
)

type (
	conn interface {
		net.Conn
		protocolHandshake(ctx context.Context) error
		pushRead(payload []byte)
	}
)
