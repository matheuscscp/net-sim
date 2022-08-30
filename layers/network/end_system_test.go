package network_test

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/matheuscscp/net-sim/layers/link"
	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/physical"
	"github.com/matheuscscp/net-sim/test"

	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	endSystemConfig = network.LayerConfig{
		DefaultRouteInterface: "eth0",
		Interfaces: []network.InterfaceConfig{
			{
				Name:        "eth0",
				IPAddress:   "1.1.1.2",
				Gateway:     "1.1.1.1",
				NetworkCIDR: "1.1.1.0/24",
				Card: link.EthernetPortConfig{
					MACAddress: "00:00:5e:00:53:aa",
					Medium: physical.FullDuplexUnreliablePortConfig{
						RecvUDPEndpoint: ":50001",
						SendUDPEndpoint: ":50101",
					},
				},
			},
		},
	}

	gatewayConfig = network.InterfaceConfig{
		ForwardingMode: true,
		Name:           "eth0",
		IPAddress:      "1.1.1.1",
		Gateway:        "1.1.1.1",
		NetworkCIDR:    "1.1.1.0/24",
		Card: link.EthernetPortConfig{
			MACAddress: "00:00:5e:01:53:aa",
			Medium: physical.FullDuplexUnreliablePortConfig{
				RecvUDPEndpoint: ":50101",
				SendUDPEndpoint: ":50001",
			},
		},
	}
)

func TestEndSystem(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var endSystem network.Layer
	var gateway network.Interface
	datagramsTargetedToTransportLayer := make(chan *gplayers.IPv4, 1024)

	defer func() {
		cancel()
		wg.Wait()
		assert.NoError(t, endSystem.Close())
		test.CloseIntfsAndFlagErrorForUnexpectedData(t, endSystem.Interfaces()...)
		test.CloseIntfsAndFlagErrorForUnexpectedData(t, gateway)
		close(datagramsTargetedToTransportLayer)
		test.FlagErrorForUnexpectedDatagrams(t, datagramsTargetedToTransportLayer)
	}()

	// start end system
	endSystem, err := network.NewLayer(ctx, endSystemConfig)
	require.NoError(t, err)
	require.NotNil(t, endSystem)
	wg.Add(1)
	go func() {
		defer wg.Done()
		endSystem.Listen(ctx, func(datagram *gplayers.IPv4) {
			datagramsTargetedToTransportLayer <- datagram
		})
	}()
	lo := endSystem.Interface("lo")
	eth0 := endSystem.Interface("eth0")

	// start gateway
	gateway, err = network.NewInterface(ctx, gatewayConfig)
	require.NoError(t, err)
	require.NotNil(t, gateway)

	// a datagram sent to the loopback interface simply loops
	// back (this interface is not backed by a real card)
	helloPayload := []byte("hello payload")
	require.NoError(t, endSystem.Send(ctx, &gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		DstIP: network.LoopbackIPAddress(),
	}))
	test.AssertDatagram(
		t,
		datagramsTargetedToTransportLayer,
		lo.IPAddress().Raw(), // src
		lo.IPAddress().Raw(), // dst
		helloPayload,
	)

	// if a default route was configured then datagrams will always
	// have an interface to go out from (even if loopback). it's
	// eth0 in this case, and the datagram will reach the gateway
	// (which is on the other end of the physical segment)
	require.NoError(t, endSystem.Send(ctx, &gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		DstIP: net.ParseIP("8.8.8.8"),
	}))
	test.AssertDatagram(
		t,
		gateway.Recv(),
		eth0.IPAddress().Raw(), // src
		net.ParseIP("8.8.8.8"), // dst
		helloPayload,
	)

	// a broadcast will be delivered to the transport layer
	require.NoError(t, gateway.Send(ctx, &gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		SrcIP: gateway.IPAddress().Raw(), // forwarding mode doesnt set src IP
		DstIP: gateway.BroadcastIPAddress().Raw(),
	}))
	test.AssertDatagram(
		t,
		datagramsTargetedToTransportLayer,
		gateway.IPAddress().Raw(),          // src
		gateway.BroadcastIPAddress().Raw(), // dst
		helloPayload,
	)
}
