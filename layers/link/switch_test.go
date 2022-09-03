package link_test

import (
	"context"
	"testing"

	"github.com/matheuscscp/net-sim/layers/link"
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
				Medium: physical.FullDuplexUnreliableWireConfig{
					RecvUDPEndpoint: ":50061",
					SendUDPEndpoint: ":50161",
				},
			},
			{
				MACAddress: "00:00:5e:00:53:ab",
				Medium: physical.FullDuplexUnreliableWireConfig{
					RecvUDPEndpoint: ":50062",
					SendUDPEndpoint: ":50162",
				},
			},
			{
				MACAddress: "00:00:5e:00:53:ac",
				Medium: physical.FullDuplexUnreliableWireConfig{
					RecvUDPEndpoint: ":50063",
					SendUDPEndpoint: ":50163",
				},
			},
		},
	}

	switchPeersConfig = []*link.EthernetPortConfig{
		{
			MACAddress: "00:00:5e:01:53:aa",
			Medium: physical.FullDuplexUnreliableWireConfig{
				RecvUDPEndpoint: ":50161",
				SendUDPEndpoint: ":50061",
			},
		},
		{
			MACAddress: "00:00:5e:01:53:ab",
			Medium: physical.FullDuplexUnreliableWireConfig{
				RecvUDPEndpoint: ":50162",
				SendUDPEndpoint: ":50062",
			},
		},
		{
			MACAddress: "00:00:5e:01:53:ac",
			Medium: physical.FullDuplexUnreliableWireConfig{
				RecvUDPEndpoint: ":50163",
				SendUDPEndpoint: ":50063",
			},
		},
	}
)

func TestSwitch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var waitClose link.SwitchWaitCloseFunc
	switchPeers := make([]link.EthernetPort, len(switchPeersConfig))

	defer func() {
		cancel()
		waitClose(func(portBuffer <-chan *gplayers.Ethernet) {
			test.FlagErrorForUnexpectedFrames(t, portBuffer)
		})
		test.CloseEthPortsAndFlagErrorForUnexpectedData(t, switchPeers...)
	}()

	// start switch
	var err error
	waitClose, err = link.RunSwitch(ctx, switchConfig)
	require.NoError(t, err)
	require.NotNil(t, waitClose)

	// start switch peers. using forwarding mode so we can assert
	// about frames with unmatched dst MAC address
	for i, portConf := range switchPeersConfig {
		portConf := *portConf
		portConf.ForwardingMode = true
		port, err := link.NewEthernetPort(ctx, portConf)
		require.NoError(t, err)
		switchPeers[i] = port
	}

	// the first frame will reach all other ports because there
	// are no entries in the switch's forwarding table
	helloPayload := []byte("hello world")
	require.NoError(t, switchPeers[0].Send(ctx, &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		SrcMAC:       switchPeers[0].MACAddress().Raw(), // forwarding mode doesnt set src mac
		DstMAC:       []byte{0, 0, 0, 0, 0, 0},          // dst mac address will not match
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(helloPayload)),
	}))
	for i := 1; i <= 2; i++ {
		test.AssertFrame(
			t,
			switchPeers[i].Recv(),
			switchPeers[0].MACAddress().Raw(), // src
			[]byte{0, 0, 0, 0, 0, 0},          // dst
			helloPayload,
		)
	}

	// now switchPeers[1] (and switchPeers[2]) can send frames directly to
	// switchPeers[0]'s mac address because the switch learned its port
	require.NoError(t, switchPeers[1].Send(ctx, &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		SrcMAC:       switchPeers[1].MACAddress().Raw(), // forwarding mode doesnt set src mac
		DstMAC:       switchPeers[0].MACAddress().Raw(),
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(helloPayload)),
	}))
	test.AssertFrame(
		t,
		switchPeers[0].Recv(),
		switchPeers[1].MACAddress().Raw(), // src
		switchPeers[0].MACAddress().Raw(), // dst
		helloPayload,
	)

	// frames with dst MAC address matching one of the switch's ports
	// are discarded. here we prove that the frame is discarded by
	// sending another frame right after the discarded one which will
	// not be discarded and compare the payload
	helloDiscardedPayload := []byte("hello world discarded")
	require.NoError(t, switchPeers[0].Send(ctx, &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloDiscardedPayload,
		},
		SrcMAC:       switchPeers[0].MACAddress().Raw(), // forwarding mode doesnt set src MAC
		DstMAC:       test.MustParseMAC(t, switchConfig.Ports[1].MACAddress),
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(helloDiscardedPayload)),
	}))
	require.NoError(t, switchPeers[1].Send(ctx, &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		SrcMAC:       switchPeers[0].MACAddress().Raw(), // forwarding mode doesnt set src MAC
		DstMAC:       switchPeers[1].MACAddress().Raw(),
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(helloPayload)),
	}))
	test.AssertFrame(
		t,
		switchPeers[1].Recv(),
		switchPeers[0].MACAddress().Raw(), // src
		switchPeers[1].MACAddress().Raw(), // dst
		helloPayload,
	)
}
