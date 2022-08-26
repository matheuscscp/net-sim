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
	routerConfig = []network.InterfaceConfig{
		{
			Name:        "eth0",
			IPAddress:   "1.1.1.1",
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
		{
			Name:        "eth1",
			IPAddress:   "1.1.2.1",
			Gateway:     "1.1.2.1",
			NetworkCIDR: "1.1.2.0/24",
			Card: link.EthernetPortConfig{
				MACAddress: "00:00:5e:00:53:ab",
				Medium: physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50002",
					SendUDPEndpoint: ":50102",
				},
			},
		},
	}

	routerPeersConfig = []*network.InterfaceConfig{
		{
			Name:        "eth0",
			IPAddress:   "1.1.1.2",
			Gateway:     "1.1.1.1",
			NetworkCIDR: "1.1.1.0/24",
			Card: link.EthernetPortConfig{
				MACAddress: "00:00:5e:01:53:aa",
				Medium: physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50101",
					SendUDPEndpoint: ":50001",
				},
			},
		},
		{
			Name:        "eth1",
			IPAddress:   "1.1.2.2",
			Gateway:     "1.1.2.1",
			NetworkCIDR: "1.1.2.0/24",
			Card: link.EthernetPortConfig{
				MACAddress: "00:00:5e:01:53:ab",
				Medium: physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50102",
					SendUDPEndpoint: ":50002",
				},
			},
		},
	}
)

func TestRouter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var routerIntfs []network.Interface
	routerPeers := make([]network.Interface, len(routerPeersConfig))
	datagramsTargetedToTransportLayer := make(chan *gplayers.IPv4)

	defer func() {
		cancel()
		wg.Wait()
		test.CloseIntfsAndFlagErrorForUnexpectedData(t, routerIntfs...)
		test.CloseIntfsAndFlagErrorForUnexpectedData(t, routerPeers...)
		close(datagramsTargetedToTransportLayer)
		test.FlagErrorForUnexpectedDatagrams(t, datagramsTargetedToTransportLayer)
	}()

	// start router
	router, err := network.NewLayer(ctx, network.LayerConfig{
		ForwardingMode: true,
		Interfaces:     routerConfig,
	})
	require.NoError(t, err)
	routerIntfs = router.Interfaces()
	wg.Add(1)
	go func() {
		defer wg.Done()
		router.Listen(ctx, func(datagram *gplayers.IPv4) {
			datagramsTargetedToTransportLayer <- datagram
		})
		assert.NoError(t, router.Close())
	}()

	// start router peers
	for i, intfConf := range routerPeersConfig {
		intfConf := *intfConf
		port, err := network.NewInterface(ctx, intfConf)
		require.NoError(t, err)
		routerPeers[i] = port
	}

	// a router must be able to forward an IP datagram from
	// one L3-segment/network to another
	helloPayload := []byte("hello world hello world")
	require.NoError(t, routerPeers[0].Send(ctx, &gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		DstIP: routerPeers[1].IPAddress().Raw(),
	}))
	test.AssertDatagram(
		t,
		routerPeers[1].Recv(),
		routerPeers[0].IPAddress().Raw(), // src
		routerPeers[1].IPAddress().Raw(), // dst
		helloPayload,
	)

	// if no routes are known for a given dst IP address, the
	// router discards the datagram. here we prove that the
	// datagram is discarded by sending another datagram right
	// after the discarded one which will not be discarded
	// and compare the payload
	require.NoError(t, routerPeers[0].Send(ctx, &gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		DstIP: net.ParseIP("8.8.8.8"),
	}))
	require.NoError(t, routerPeers[0].Send(ctx, &gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		DstIP: routerPeers[1].IPAddress().Raw(),
	}))
	test.AssertDatagram(
		t,
		routerPeers[1].Recv(),
		routerPeers[0].IPAddress().Raw(), // src
		routerPeers[1].IPAddress().Raw(), // dst
		helloPayload,
	)
}
