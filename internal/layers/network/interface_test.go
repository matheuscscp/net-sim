package network_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/matheuscscp/net-sim/internal/layers/link"
	"github.com/matheuscscp/net-sim/internal/layers/network"
	"github.com/matheuscscp/net-sim/internal/layers/physical"
	"github.com/matheuscscp/net-sim/test"

	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	switchConfig = link.SwitchConfig{
		Ports: []*link.EthernetPortConfig{
			{
				MACAddress: "00:00:5e:00:53:aa",
				Medium: &physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50001",
					SendUDPEndpoint: ":50101",
				},
			},
			{
				MACAddress: "00:00:5e:00:53:ab",
				Medium: &physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50002",
					SendUDPEndpoint: ":50102",
				},
			},
			{
				MACAddress: "00:00:5e:00:53:ac",
				Medium: &physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50003",
					SendUDPEndpoint: ":50103",
				},
			},
		},
	}

	interfacesConfig = []*network.InterfaceConfig{
		{
			ForwardingMode: true, // gateway
			IPAddress:      "1.1.1.1",
			Card: &link.EthernetPortConfig{
				MACAddress: "00:00:5e:01:53:aa",
				Medium: &physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50101",
					SendUDPEndpoint: ":50001",
				},
			},
		},
		{
			IPAddress: "1.1.1.2",
			Card: &link.EthernetPortConfig{
				MACAddress: "00:00:5e:01:53:ab",
				Medium: &physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50102",
					SendUDPEndpoint: ":50002",
				},
			},
		},
		{
			IPAddress: "1.1.1.3",
			Card: &link.EthernetPortConfig{
				MACAddress: "00:00:5e:01:53:ac",
				Medium: &physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50103",
					SendUDPEndpoint: ":50003",
				},
			},
		},
	}
)

func TestLocalAreaNetwork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var waitCloseSwitch func()
	interfaces := make([]network.Interface, len(interfacesConfig))
	ports := make([]link.EthernetPort, len(interfacesConfig))
	switchPorts := make([]link.EthernetPort, len(switchConfig.Ports))

	defer func() {
		cancel()
		waitCloseSwitch()
		for i, intf := range interfaces {
			assert.NoError(t, intf.Close())
			test.FlagErrorForUnexpectedDatagrams(t, intf.Recv())
			// the interface closes the port
			port := ports[i]
			test.FlagErrorForUnexpectedFrames(t, port.Recv())
		}
		for _, port := range switchPorts {
			// the switch closes the ports
			test.FlagErrorForUnexpectedFrames(t, port.Recv())
		}
	}()

	// start ports and interfaces
	for i, intfConf := range interfacesConfig {
		portConf := intfConf.Card
		port, err := link.NewEthernetPort(ctx, *portConf)
		require.NoError(t, err)
		ports[i] = port

		intfConf := *intfConf
		intfConf.Gateway = "1.1.1.1"
		intfConf.NetworkCIDR = "1.1.1.0/24"
		intfConf.Card = nil
		intf, err := network.NewInterface(ctx, intfConf, port)
		require.NoError(t, err)
		interfaces[i] = intf
	}

	// start switch ports and switch
	for i, portConf := range switchConfig.Ports {
		portConf := *portConf
		portConf.ForwardingMode = true
		port, err := link.NewEthernetPort(ctx, portConf)
		require.NoError(t, err)
		switchPorts[i] = port
	}
	var err error
	waitCloseSwitch, err = link.RunSwitch(ctx, link.SwitchConfig{}, switchPorts...)
	require.NoError(t, err)
	require.NotNil(t, waitCloseSwitch)

	// the first datagram will broadcast an ARP request (reaching
	// interfaces[0] and interfaces[2], hence they will both learn
	// an L2 route for interfaces[1]), but only interfaces[2] will
	// send an ARP reply, directly to interfaces[1]
	helloPayload := []byte("hello world hello world")
	require.NoError(t, interfaces[1].Send(ctx, &gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		DstIP: interfaces[2].IPAddress().Raw(),
	}))
	time.Sleep(110 * time.Millisecond) // datagram will be delayed waiting for ARP
	test.AssertDatagram(
		t,
		interfaces[2].Recv(),
		interfaces[1].IPAddress().Raw(), // src
		interfaces[2].IPAddress().Raw(), // dst
		helloPayload,
	)

	// a datagram with dst IP address outside the network will arrive
	// at the gateway
	publicIPAddress := net.ParseIP("8.8.8.8")
	require.NoError(t, interfaces[1].Send(ctx, &gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		DstIP: publicIPAddress,
	}))
	time.Sleep(110 * time.Millisecond) // datagram will be delayed waiting for ARP
	test.AssertDatagram(
		t,
		interfaces[0].Recv(),
		interfaces[1].IPAddress().Raw(), // src
		publicIPAddress,                 // dst
		helloPayload,
	)

	// a broadcast to the network will reach all interfaces
	broadcastIPAddress := net.ParseIP("1.1.1.255")
	require.NoError(t, interfaces[2].Send(ctx, &gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		DstIP: broadcastIPAddress,
	}))
	for i := 0; i <= 1; i++ {
		test.AssertDatagram(
			t,
			interfaces[i].Recv(),
			interfaces[2].IPAddress().Raw(), // src
			broadcastIPAddress,              // dst
			helloPayload,
		)
	}
}
