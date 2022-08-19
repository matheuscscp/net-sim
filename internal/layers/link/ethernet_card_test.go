package link_test

import (
	"context"
	"net"
	"testing"

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

	assertFrame := func(
		ch <-chan *gplayers.Ethernet,
		src, dst net.HardwareAddr,
		payload []byte,
	) {
		for eth := range ch {
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
			assert.Equal(t, expected, eth)
			break
		}
	}
	assertFrame(card1Recv, card2MAC, card1MAC, card2Payload)
	assertFrame(card2Recv, card1MAC, card2MAC, card1Payload)

	card1.ShutdownRecv()
	card2.ShutdownRecv()

	<-card1Recv
	<-card2Recv
	if err := <-card1Err; err != nil {
		t.Error(err)
	}
	if err := <-card2Err; err != nil {
		t.Error(err)
	}

	assert.NoError(t, card1.Close())
	assert.NoError(t, card2.Close())
}
