package transport

import (
	"context"
	"net"
)

type (
	hostLayer struct{}
)

// NewHostLayer returns an implementation of Layer with Go's standard
// library "net" (using the OS native sockets).
func NewHostLayer() Layer {
	return &hostLayer{}
}

func (h *hostLayer) Listen(network, address string) (net.Listener, error) {
	return net.Listen(network, address)
}

func (h *hostLayer) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func (h *hostLayer) Close() error {
	return nil
}
