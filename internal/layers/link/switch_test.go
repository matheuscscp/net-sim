package link_test

import (
	"context"
	"testing"
	"time"

	"github.com/matheuscscp/net-sim/internal/layers/link"
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

	switchPeersConfig = []*link.EthernetPortConfig{
		{
			MACAddress: "00:00:5e:01:53:aa",
			Medium: &physical.FullDuplexUnreliablePortConfig{
				RecvUDPEndpoint: ":50101",
				SendUDPEndpoint: ":50001",
			},
		},
		{
			MACAddress: "00:00:5e:01:53:ab",
			Medium: &physical.FullDuplexUnreliablePortConfig{
				RecvUDPEndpoint: ":50102",
				SendUDPEndpoint: ":50002",
			},
		},
		{
			MACAddress: "00:00:5e:01:53:ac",
			Medium: &physical.FullDuplexUnreliablePortConfig{
				RecvUDPEndpoint: ":50103",
				SendUDPEndpoint: ":50003",
			},
		},
	}
)

func TestSwitch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var waitClose func()
	switchPorts := make([]link.EthernetPort, len(switchConfig.Ports))
	switchPeers := make([]link.EthernetPort, len(switchPeersConfig))

	defer func() {
		cancel()
		waitClose()
		for _, port := range switchPorts {
			// the switch closes the ports
			test.FlagErrorForUnexpectedFrames(t, port.Recv())
		}
		for _, port := range switchPeers {
			assert.NoError(t, port.Close())
			test.FlagErrorForUnexpectedFrames(t, port.Recv())
		}
	}()

	// start switch ports and switch
	for i, portConf := range switchConfig.Ports {
		portConf := *portConf
		portConf.ForwardingMode = true
		port, err := link.NewEthernetPort(ctx, portConf)
		require.NoError(t, err)
		switchPorts[i] = port
	}
	var err error
	waitClose, err = link.RunSwitch(ctx, link.SwitchConfig{}, switchPorts...)
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

	// frames with dst mac address matching one of the switch's ports
	// are discarded
	require.NoError(t, switchPeers[0].Send(ctx, &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		SrcMAC:       switchPeers[0].MACAddress().Raw(), // forwarding mode doesnt set src mac
		DstMAC:       test.MustParseMAC(t, switchConfig.Ports[1].MACAddress),
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(helloPayload)),
	}))
	time.Sleep(100 * time.Millisecond) // give time for frame to arrive and be discarded
}
