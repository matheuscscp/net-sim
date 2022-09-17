package hostnetwork

import (
	"context"
	"net"

	"github.com/matheuscscp/net-sim/layers/transport"
)

type (
	transportLayer struct {
		closed bool
	}
)

// NewTransportLayer returns an implementation of transport.Layer backed
// by Go's standard library "net" (hence on the host network).
func NewTransportLayer() transport.Layer {
	return &transportLayer{}
}

func (t *transportLayer) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	if t.closed {
		return nil, transport.ErrProtocolClosed
	}
	return (&net.ListenConfig{}).Listen(ctx, network, address)
}

func (t *transportLayer) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	if t.closed {
		return nil, transport.ErrProtocolClosed
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func (t *transportLayer) Close() error {
	t.closed = true
	return nil
}
