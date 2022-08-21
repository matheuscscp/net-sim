package link_test

import (
	"context"
	"testing"

	"github.com/matheuscscp/net-sim/internal/layers/link"
	"github.com/matheuscscp/net-sim/internal/layers/physical"

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

	// using forwarding mode so we can capture frames with unmatched dst MAC address
	switchPeersConfig = []*link.EthernetPortConfig{
		{
			ForwardingMode: true,
			MACAddress:     "00:00:5e:01:53:aa",
			Medium: &physical.FullDuplexUnreliablePortConfig{
				RecvUDPEndpoint: ":50101",
				SendUDPEndpoint: ":50001",
			},
		},
		{
			ForwardingMode: true,
			MACAddress:     "00:00:5e:01:53:ab",
			Medium: &physical.FullDuplexUnreliablePortConfig{
				RecvUDPEndpoint: ":50102",
				SendUDPEndpoint: ":50002",
			},
		},
		{
			ForwardingMode: true,
			MACAddress:     "00:00:5e:01:53:ac",
			Medium: &physical.FullDuplexUnreliablePortConfig{
				RecvUDPEndpoint: ":50103",
				SendUDPEndpoint: ":50003",
			},
		},
	}
)

func TestSwitch(t *testing.T) {
	switchPeers := make([]link.EthernetPort, 0, len(switchPeersConfig))

	switchErr := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		assert.NoError(t, <-switchErr)
		close(switchErr)
		for _, port := range switchPeers {
			assert.NoError(t, port.Close())
			flagErrorForUnexpectedFrames(t, port.Recv())
		}
	}()

	go func() {
		switchErr <- link.RunSwitch(ctx, switchConfig)
	}()

	for _, portConf := range switchPeersConfig {
		port, err := link.NewEthernetPort(ctx, *portConf)
		require.NoError(t, err)
		switchPeers = append(switchPeers, port)
	}

	// the first frame will reach all other ports because there
	// are no entries in the switch's forwarding table
	helloPayload := []byte("hello world")
	require.NoError(t, switchPeers[0].Send(ctx, &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		SrcMAC:       switchPeers[0].MACAddress().Raw(),
		DstMAC:       []byte{0, 0, 0, 0, 0, 0},
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(helloPayload)),
	}))
	for i := 1; i <= 2; i++ {
		assertFrame(
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
		SrcMAC:       switchPeers[1].MACAddress().Raw(),
		DstMAC:       switchPeers[0].MACAddress().Raw(),
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(helloPayload)),
	}))
	assertFrame(
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
		SrcMAC:       switchPeers[0].MACAddress().Raw(),
		DstMAC:       mustParseMAC(t, switchConfig.Ports[1].MACAddress),
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(helloPayload)),
	}))
}
