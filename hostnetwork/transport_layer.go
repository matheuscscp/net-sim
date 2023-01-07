package hostnetwork

import (
	"context"
	"fmt"
	"net"

	"github.com/matheuscscp/net-sim/layers/transport"
)

type (
	transportLayer struct {
		closed bool
	}

	transportLayerDialer struct {
		*transportLayer
		*net.Dialer
	}
)

// NewTransportLayer returns an implementation of transport.Layer backed
// by Go's standard library "net" (hence on the host network).
func NewTransportLayer() transport.Layer {
	return &transportLayer{}
}

func (t *transportLayer) Listen(ctx context.Context, network, localAddr string) (net.Listener, error) {
	if t.closed {
		return nil, transport.ErrProtocolClosed
	}
	return (&net.ListenConfig{}).Listen(ctx, network, localAddr)
}

func (t *transportLayer) Dial(ctx context.Context, network, remoteAddr string) (net.Conn, error) {
	return t.dial(ctx, &net.Dialer{}, network, remoteAddr)
}

func (t *transportLayer) dial(ctx context.Context, dialer *net.Dialer, network, remoteAddr string) (net.Conn, error) {
	if t.closed {
		return nil, transport.ErrProtocolClosed
	}
	return dialer.DialContext(ctx, network, remoteAddr)
}

func (t *transportLayer) Dialer() transport.LayerDialer {
	return &transportLayerDialer{
		transportLayer: t,
		Dialer:         &net.Dialer{},
	}
}

func (t *transportLayer) Close() error {
	t.closed = true
	return nil
}

func (d *transportLayerDialer) WithLocalAddr(localAddr net.Addr) transport.LayerDialer {
	d.Dialer.LocalAddr = localAddr
	return d
}

func (d *transportLayerDialer) Dial(ctx context.Context, remoteAddr string) (net.Conn, error) {
	if d.Dialer.LocalAddr == nil {
		return nil, fmt.Errorf("localAddr was not specified")
	}
	return d.transportLayer.dial(ctx, d.Dialer, d.Dialer.LocalAddr.Network(), remoteAddr)
}
