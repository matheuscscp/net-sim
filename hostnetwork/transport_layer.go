package hostnetwork

import (
	"context"
	"net"

	"github.com/matheuscscp/net-sim/layers/transport"
)

type (
	transportLayer struct{}
)

// NewTransportLayer returns an implementation of transport.Layer backed
// by Go's standard library "net" (hence on the host network).
func NewTransportLayer() transport.Layer {
	return transportLayer{}
}

func (transportLayer) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	var lc net.ListenConfig
	return lc.Listen(ctx, network, address)
}

func (transportLayer) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func (transportLayer) Close() error {
	return nil
}
