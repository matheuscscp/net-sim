package link_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/matheuscscp/net-sim/internal/layers/link"
	"github.com/matheuscscp/net-sim/internal/layers/physical"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateConnectedCards(t *testing.T) {
	card1MAC, err := net.ParseMAC("00:00:5e:00:53:ae")
	require.NoError(t, err)
	card1, err := link.NewEthernetCard(context.Background(), link.EthernetCardConfig{
		MACAddress: card1MAC.String(),
		Medium: &physical.FullDuplexUnreliablePortConfig{
			RecvUDPEndpoint: ":50001",
			SendUDPEndpoint: ":50002",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, card1)
	card1Recv, card1Err := card1.Recv()

	card2MAC, err := net.ParseMAC("00:00:5e:00:53:af")
	require.NoError(t, err)
	card2, err := link.NewEthernetCard(context.Background(), link.EthernetCardConfig{
		MACAddress: card2MAC.String(),
		Medium: &physical.FullDuplexUnreliablePortConfig{
			RecvUDPEndpoint: ":50002",
			SendUDPEndpoint: ":50001",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, card2)
	card2Recv, card2Err := card2.Recv()

	card1Payload := []byte("hello card2")
	require.NoError(t, card1.Send(context.Background(), &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: card1Payload,
		},
		DstMAC:       card2MAC,
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(card1Payload)),
	}))

	card2Payload := []byte("hello card1")
	require.NoError(t, card2.Send(context.Background(), &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: card2Payload,
		},
		DstMAC:       card1MAC,
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(card2Payload)),
	}))

	assertFrame(t, card1Recv, card2MAC, card1MAC, card2Payload)
	assertFrame(t, card2Recv, card1MAC, card2MAC, card1Payload)

	assert.NoError(t, card1.Close())
	assert.NoError(t, card2.Close())

	drainChannels(t, card1Recv, card1Err)
	drainChannels(t, card2Recv, card2Err)
}

func TestWrongDstMACAddress(t *testing.T) {
	card1MAC, err := net.ParseMAC("00:00:5e:00:53:ae")
	require.NoError(t, err)
	card1, err := link.NewEthernetCard(context.Background(), link.EthernetCardConfig{
		MACAddress: card1MAC.String(),
		Medium: &physical.FullDuplexUnreliablePortConfig{
			RecvUDPEndpoint: ":50001",
			SendUDPEndpoint: ":50002",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, card1)
	card1Recv, card1Err := card1.Recv()

	card2MAC, err := net.ParseMAC("00:00:5e:00:53:af")
	require.NoError(t, err)
	card2, err := link.NewEthernetCard(context.Background(), link.EthernetCardConfig{
		MACAddress: card2MAC.String(),
		Medium: &physical.FullDuplexUnreliablePortConfig{
			RecvUDPEndpoint: ":50002",
			SendUDPEndpoint: ":50001",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, card2)
	card2Recv, card2Err := card2.Recv()

	card1Payload := []byte("hello card2")
	require.NoError(t, card1.Send(context.Background(), &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: card1Payload,
		},
		DstMAC:       []byte{0x0, 0x0, 0x0, 0x0, 0x0, 0x0},
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(card1Payload)),
	}))

	time.Sleep(100 * time.Millisecond) // give time for frame to arrive and be discarded
	assert.NoError(t, card1.Close())
	assert.NoError(t, card2.Close())

	drainChannels(t, card1Recv, card1Err)
	drainChannels(t, card2Recv, card2Err)
}

func TestForwardingMode(t *testing.T) {
	card1MAC, err := net.ParseMAC("00:00:5e:00:53:ae")
	require.NoError(t, err)
	card1, err := link.NewEthernetCard(context.Background(), link.EthernetCardConfig{
		MACAddress: card1MAC.String(),
		Medium: &physical.FullDuplexUnreliablePortConfig{
			RecvUDPEndpoint: ":50001",
			SendUDPEndpoint: ":50002",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, card1)
	card1Recv, card1Err := card1.Recv()

	card2MAC, err := net.ParseMAC("00:00:5e:00:53:af")
	require.NoError(t, err)
	card2, err := link.NewEthernetCard(context.Background(), link.EthernetCardConfig{
		ForwardingMode: true,
		MACAddress:     card2MAC.String(),
		Medium: &physical.FullDuplexUnreliablePortConfig{
			RecvUDPEndpoint: ":50002",
			SendUDPEndpoint: ":50001",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, card2)
	card2Recv, card2Err := card2.Recv()

	card1Payload := []byte("hello card2")
	require.NoError(t, card1.Send(context.Background(), &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: card1Payload,
		},
		DstMAC:       card2MAC,
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(card1Payload)),
	}))

	assertFrame(t, card2Recv, card1MAC, card2MAC, card1Payload)

	assert.NoError(t, card1.Close())
	assert.NoError(t, card2.Close())

	drainChannels(t, card1Recv, card1Err)
	drainChannels(t, card2Recv, card2Err)
}

func assertFrame(
	t *testing.T,
	ch <-chan *gplayers.Ethernet,
	src, dst net.HardwareAddr,
	payload []byte,
) {
	expected := &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: payload,
		},
		SrcMAC:       src,
		DstMAC:       dst,
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(payload)),
	}
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{}
	require.NoError(t, expected.SerializeTo(buf, opts))
	expected.Contents = gopacket.
		NewPacket(buf.Bytes(), gplayers.LayerTypeEthernet, gopacket.Default).
		LinkLayer().
		LayerContents()
	assert.Equal(t, expected, <-ch)
}

func drainChannels(t *testing.T, ch <-chan *gplayers.Ethernet, errCh <-chan error) {
	for eth := range ch {
		t.Errorf("received more ethernet frames than expected: %+v", eth)
	}
	for err := range errCh {
		t.Error(err)
	}
}
