package network_test

import (
	"context"
	"net"
	"testing"

	"github.com/matheuscscp/net-sim/layers/link"
	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/physical"
	"github.com/matheuscscp/net-sim/test"

	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/require"
)

var (
	switchConfig = link.SwitchConfig{
		Ports: []link.EthernetPortConfig{
			{
				MACAddress: "00:00:5e:00:53:aa",
				Medium: physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50001",
					SendUDPEndpoint: ":50101",
				},
			},
			{
				MACAddress: "00:00:5e:00:53:ab",
				Medium: physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50002",
					SendUDPEndpoint: ":50102",
				},
			},
			{
				MACAddress: "00:00:5e:00:53:ac",
				Medium: physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50003",
					SendUDPEndpoint: ":50103",
				},
			},
		},
	}

	interfacesConfig = []*network.InterfaceConfig{
		{
			ForwardingMode: true, // gateway
			Name:           "eth0",
			IPAddress:      "1.1.1.1",
			Card: link.EthernetPortConfig{
				MACAddress: "00:00:5e:01:53:aa",
				Medium: physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50101",
					SendUDPEndpoint: ":50001",
				},
			},
		},
		{
			Name:      "eth1",
			IPAddress: "1.1.1.2",
			Card: link.EthernetPortConfig{
				MACAddress: "00:00:5e:01:53:ab",
				Medium: physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50102",
					SendUDPEndpoint: ":50002",
				},
			},
		},
		{
			Name:      "eth2",
			IPAddress: "1.1.1.3",
			Card: link.EthernetPortConfig{
				MACAddress: "00:00:5e:01:53:ac",
				Medium: physical.FullDuplexUnreliablePortConfig{
					RecvUDPEndpoint: ":50103",
					SendUDPEndpoint: ":50003",
				},
			},
		},
	}
)

func TestLocalAreaNetwork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var waitCloseSwitch link.SwitchWaitCloseFunc
	intfs := make([]network.Interface, len(interfacesConfig))

	defer func() {
		cancel()
		waitCloseSwitch(func(portBuffer <-chan *gplayers.Ethernet) {
			test.FlagErrorForUnexpectedFrames(t, portBuffer)
		})
		test.CloseIntfsAndFlagErrorForUnexpectedData(t, intfs...)
	}()

	// start interfaces
	for i, intfConf := range interfacesConfig {
		intfConf := *intfConf
		intfConf.Gateway = "1.1.1.1"
		intfConf.NetworkCIDR = "1.1.1.0/24"
		intf, err := network.NewInterface(ctx, intfConf)
		require.NoError(t, err)
		intfs[i] = intf
	}

	// start switch
	var err error
	waitCloseSwitch, err = link.RunSwitch(ctx, switchConfig)
	require.NoError(t, err)
	require.NotNil(t, waitCloseSwitch)

	// the first datagram will broadcast an ARP request (reaching
	// intfs[0] and intfs[2], hence they will both learn
	// an L2 route for intfs[1]), but only intfs[2] will
	// send an ARP reply, directly to intfs[1]
	helloPayload := []byte("hello world hello world")
	require.NoError(t, intfs[1].Send(ctx, &gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		DstIP: intfs[2].IPAddress().Raw(),
	}))
	test.AssertDatagram(
		t,
		intfs[2].Recv(),
		intfs[1].IPAddress().Raw(), // src
		intfs[2].IPAddress().Raw(), // dst
		helloPayload,
	)

	// a datagram with dst IP address outside the network will arrive
	// at the gateway
	publicIPAddress := net.ParseIP("8.8.8.8")
	require.NoError(t, intfs[1].Send(ctx, &gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		DstIP: publicIPAddress,
	}))
	test.AssertDatagram(
		t,
		intfs[0].Recv(),
		intfs[1].IPAddress().Raw(), // src
		publicIPAddress,            // dst
		helloPayload,
	)

	// a broadcast to the network will reach all interfaces
	broadcastIPAddress := net.ParseIP("1.1.1.255")
	require.NoError(t, intfs[2].Send(ctx, &gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		DstIP: broadcastIPAddress,
	}))
	for i := 0; i <= 1; i++ {
		test.AssertDatagram(
			t,
			intfs[i].Recv(),
			intfs[2].IPAddress().Raw(), // src
			broadcastIPAddress,         // dst
			helloPayload,
		)
	}

	// a datagram arriving with wrong dst IP address is discarded.
	// here we prove that the datagram is discarded by sending another
	// datagram right after the discarded one which will not be discarded
	// and compare the payload. in case the switch forwards the datagram
	// with wrong IP/MAC address to both intfs[0] and intfs[2], we send
	// a correct datagram to both interfaces afterwards
	helloDiscardedPayload := []byte("hello world discarded")
	datagramBuf, err := network.SerializeDatagram(&gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloDiscardedPayload,
		},
		SrcIP: intfs[1].IPAddress().Raw(),
		DstIP: intfs[0].IPAddress().Raw(),
	})
	require.NoError(t, err)
	require.NoError(t, intfs[1].Card().Send(ctx, &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: datagramBuf,
		},
		DstMAC:       intfs[2].Card().MACAddress().Raw(),
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(datagramBuf)),
	}))
	for _, dstIntf := range []network.Interface{intfs[0], intfs[2]} {
		require.NoError(t, intfs[1].Send(ctx, &gplayers.IPv4{
			BaseLayer: gplayers.BaseLayer{
				Payload: helloPayload,
			},
			DstIP: dstIntf.IPAddress().Raw(),
		}))
		test.AssertDatagram(
			t,
			dstIntf.Recv(),
			intfs[1].IPAddress().Raw(), // src
			dstIntf.IPAddress().Raw(),  // dst
			helloPayload,
		)
	}
}
