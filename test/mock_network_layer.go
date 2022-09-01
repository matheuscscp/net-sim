package test

import (
	"context"
	"testing"

	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/transport"

	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/require"
)

type (
	mockNetworkLayer struct {
		t              *testing.T
		sentSegments   chan<- *gplayers.UDP
		recvdDatagrams <-chan *gplayers.IPv4
	}
)

// NewMockNetworkLayer creates a mock network.Layer that simply
// relays datagrams received on the recvDatagrams channel to
// the listener.
func NewMockNetworkLayer(
	t *testing.T,
	sentSegments chan<- *gplayers.UDP,
	recvdDatagrams <-chan *gplayers.IPv4,
) network.Layer {
	return &mockNetworkLayer{t, sentSegments, recvdDatagrams}
}

func (m *mockNetworkLayer) Send(ctx context.Context, datagram *gplayers.IPv4) error {
	segment, err := transport.DeserializeUDPSegment(datagram.Payload)
	require.NoError(m.t, err)
	require.NotNil(m.t, segment)
	m.sentSegments <- segment
	return nil
}

func (m *mockNetworkLayer) ForwardingMode() bool {
	return false
}

func (m *mockNetworkLayer) ForwardingTable() *network.ForwardingTable {
	return nil
}

func (m *mockNetworkLayer) Interfaces() []network.Interface {
	return nil
}

func (m *mockNetworkLayer) Interface(name string) network.Interface {
	return nil
}

func (m *mockNetworkLayer) Listen(ctx context.Context, listener func(datagram *gplayers.IPv4)) {
	ctxDone := ctx.Done()
	for {
		select {
		case <-ctxDone:
			return
		case datagram := <-m.recvdDatagrams:
			listener(datagram)
		}
	}
}

func (m *mockNetworkLayer) Close() error {
	return nil
}
