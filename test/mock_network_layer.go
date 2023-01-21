package test

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/matheuscscp/net-sim/layers/link"
	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/transport"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/require"
)

type (
	mockNetworkLayer struct {
		t              *testing.T
		cancelCtx      context.CancelFunc
		wg             sync.WaitGroup
		intf           *mockNetworkInterface
		recvdDatagrams <-chan *gplayers.IPv4
		protocols      map[gplayers.IPProtocol]network.IPProtocol
	}

	mockNetworkInterface struct {
		t            *testing.T
		sentSegments chan<- gopacket.TransportLayer
	}
)

// NewMockNetworkLayer creates a mock network.Layer that simply
// relays datagrams received on the recvdDatagrams channel to
// the listener.
func NewMockNetworkLayer(
	t *testing.T,
	sentSegments chan<- gopacket.TransportLayer,
	recvdDatagrams <-chan *gplayers.IPv4,
) network.Layer {
	ctx, cancel := context.WithCancel(context.Background())
	m := &mockNetworkLayer{
		t:              t,
		cancelCtx:      cancel,
		intf:           &mockNetworkInterface{t, sentSegments},
		recvdDatagrams: recvdDatagrams,
		protocols:      map[gplayers.IPProtocol]network.IPProtocol{},
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ctxDone := ctx.Done()
		for {
			select {
			case <-ctxDone:
				return
			case datagram := <-m.recvdDatagrams:
				if protocol, ok := m.GetRegisteredProtocol(datagram.Protocol); ok {
					protocol.Recv(datagram)
				}
			}
		}
	}()
	return m
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

func (m *mockNetworkLayer) RegisterProtocol(protocol network.IPProtocol) {
	m.protocols[protocol.GetID()] = protocol
}

func (m *mockNetworkLayer) DeregisterProtocol(protocolID gplayers.IPProtocol) bool {
	_, ok := m.protocols[protocolID]
	delete(m.protocols, protocolID)
	return ok
}

func (m *mockNetworkLayer) GetRegisteredProtocol(protocolID gplayers.IPProtocol) (network.IPProtocol, bool) {
	protocol, ok := m.protocols[protocolID]
	return protocol, ok
}

func (m *mockNetworkLayer) Close() error {
	m.cancelCtx()
	m.wg.Wait()
	return nil
}

func (m *mockNetworkLayer) StackName() string {
	return "test"
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
