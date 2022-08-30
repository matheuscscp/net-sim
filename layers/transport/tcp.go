package transport

import (
	"context"
	"net"

	"github.com/matheuscscp/net-sim/layers/network"

	gplayers "github.com/google/gopacket/layers"
)

type (
	tcp struct {
		networkLayer      network.Layer
		transportLayerCtx context.Context
	}
)

func (t *tcp) init() {
	// TODO
}

func (t *tcp) listen(address string) (net.Listener, error) {
	return nil, nil // TODO
}

func (t *tcp) dial(ctx context.Context, address string) (net.Conn, error) {
	return nil, nil // TODO
}

func (t *tcp) decapAndDemux(datagram *gplayers.IPv4) {
	// TODO
}
