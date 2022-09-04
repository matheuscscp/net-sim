package test

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/matheuscscp/net-sim/layers/link"
	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/transport"
	"github.com/stretchr/testify/require"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

type (
	mockNetworkLayer struct {
		t              *testing.T
		intf           *mockNetworkInterface
		recvdDatagrams <-chan *gplayers.IPv4
	}

	mockNetworkInterface struct {
		t            *testing.T
		sentSegments chan<- gopacket.TransportLayer
	}
)

// NewMockNetworkLayer creates a mock network.Layer that simply
// relays datagrams received on the recvDatagrams channel to
// the listener.
func NewMockNetworkLayer(
	t *testing.T,
	sentSegments chan<- gopacket.TransportLayer,
	recvdDatagrams <-chan *gplayers.IPv4,
) network.Layer {
	return &mockNetworkLayer{t, &mockNetworkInterface{t, sentSegments}, recvdDatagrams}
}

func (m *mockNetworkLayer) Send(ctx context.Context, datagram *gplayers.IPv4) error {
	return m.intf.Send(ctx, datagram)
}

func (m *mockNetworkLayer) FindInterfaceForHeader(datagramHeader *gplayers.IPv4) (network.Interface, error) {
	return m.intf, nil
}

func (m *mockNetworkLayer) ForwardingMode() bool {
	return false
}

func (m *mockNetworkLayer) ForwardingTable() *network.ForwardingTable {
	return nil
}

func (m *mockNetworkLayer) Interfaces() []network.Interface {
	return []network.Interface{m.intf}
}

func (m *mockNetworkLayer) Interface(name string) network.Interface {
	return m.intf
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

func (m *mockNetworkInterface) Send(ctx context.Context, datagram *gplayers.IPv4) error {
	return nil
}

func (m *mockNetworkInterface) SendTransportSegment(
	ctx context.Context,
	datagramHeader *gplayers.IPv4,
	segment gopacket.TransportLayer,
) error {
	datagramHeader.SrcIP = m.IPAddress().Raw()
	buf, err := network.SerializeDatagramWithTransportSegment(datagramHeader, segment)
	require.NoError(m.t, err)
	require.NotEmpty(m.t, buf)

	datagram, err := network.DeserializeDatagram(buf)
	require.NoError(m.t, err)
	require.NotNil(m.t, datagram)

	switch datagramHeader.Protocol {
	case gplayers.IPProtocolUDP:
		segment, err = transport.DeserializeUDPSegment(datagram)
	default:
		err = fmt.Errorf("protocol %s not implemented", datagramHeader.Protocol)
	}
	require.NoError(m.t, err)
	require.NotNil(m.t, segment)

	m.sentSegments <- segment
	return nil
}

func (m *mockNetworkInterface) Recv() <-chan *gplayers.IPv4 {
	return nil
}

func (m *mockNetworkInterface) Close() error {
	return nil
}

func (m *mockNetworkInterface) ForwardingMode() bool {
	return false
}

func (m *mockNetworkInterface) Name() string {
	return "eth0"
}

func (m *mockNetworkInterface) IPAddress() gopacket.Endpoint {
	return gplayers.NewIPEndpoint(net.ParseIP("127.0.0.1"))
}

func (m *mockNetworkInterface) Gateway() gopacket.Endpoint {
	return gopacket.Endpoint{}
}

func (m *mockNetworkInterface) Network() *net.IPNet {
	return nil
}

func (m *mockNetworkInterface) BroadcastIPAddress() gopacket.Endpoint {
	return gopacket.Endpoint{}
}

func (m *mockNetworkInterface) Card() link.EthernetPort {
	return nil
}
