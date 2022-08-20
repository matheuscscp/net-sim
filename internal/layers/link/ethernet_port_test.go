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

func TestCreateConnectedPorts(t *testing.T) {
	port1MAC, err := net.ParseMAC("00:00:5e:00:53:ae")
	require.NoError(t, err)
	port1, err := link.NewEthernetPort(context.Background(), link.EthernetPortConfig{
		MACAddress: port1MAC.String(),
		Medium: &physical.FullDuplexUnreliablePortConfig{
			RecvUDPEndpoint: ":50001",
			SendUDPEndpoint: ":50002",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, port1)
	port1Recv := port1.Recv()

	port2MAC, err := net.ParseMAC("00:00:5e:00:53:af")
	require.NoError(t, err)
	port2, err := link.NewEthernetPort(context.Background(), link.EthernetPortConfig{
		MACAddress: port2MAC.String(),
		Medium: &physical.FullDuplexUnreliablePortConfig{
			RecvUDPEndpoint: ":50002",
			SendUDPEndpoint: ":50001",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, port2)
	port2Recv := port2.Recv()

	port1Payload := []byte("hello port2")
	require.NoError(t, port1.Send(context.Background(), &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: port1Payload,
		},
		DstMAC:       port2MAC,
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(port1Payload)),
	}))

	port2Payload := []byte("hello port1")
	require.NoError(t, port2.Send(context.Background(), &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: port2Payload,
		},
		DstMAC:       port1MAC,
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(port2Payload)),
	}))

	assertFrame(t, port1Recv, port2MAC, port1MAC, port2Payload)
	assertFrame(t, port2Recv, port1MAC, port2MAC, port1Payload)

	assert.NoError(t, port1.Close())
	assert.NoError(t, port2.Close())

	drainChannels(t, port1Recv)
	drainChannels(t, port2Recv)
}

func TestWrongDstMACAddress(t *testing.T) {
	port1MAC, err := net.ParseMAC("00:00:5e:00:53:ae")
	require.NoError(t, err)
	port1, err := link.NewEthernetPort(context.Background(), link.EthernetPortConfig{
		MACAddress: port1MAC.String(),
		Medium: &physical.FullDuplexUnreliablePortConfig{
			RecvUDPEndpoint: ":50001",
			SendUDPEndpoint: ":50002",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, port1)
	port1Recv := port1.Recv()

	port2MAC, err := net.ParseMAC("00:00:5e:00:53:af")
	require.NoError(t, err)
	port2, err := link.NewEthernetPort(context.Background(), link.EthernetPortConfig{
		MACAddress: port2MAC.String(),
		Medium: &physical.FullDuplexUnreliablePortConfig{
			RecvUDPEndpoint: ":50002",
			SendUDPEndpoint: ":50001",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, port2)
	port2Recv := port2.Recv()

	port1Payload := []byte("hello port2")
	require.NoError(t, port1.Send(context.Background(), &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: port1Payload,
		},
		DstMAC:       []byte{0x0, 0x0, 0x0, 0x0, 0x0, 0x0},
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(port1Payload)),
	}))

	time.Sleep(100 * time.Millisecond) // give time for frame to arrive and be disported
	assert.NoError(t, port1.Close())
	assert.NoError(t, port2.Close())

	drainChannels(t, port1Recv)
	drainChannels(t, port2Recv)
}

func TestForwardingMode(t *testing.T) {
	port1MAC, err := net.ParseMAC("00:00:5e:00:53:ae")
	require.NoError(t, err)
	port1, err := link.NewEthernetPort(context.Background(), link.EthernetPortConfig{
		MACAddress: port1MAC.String(),
		Medium: &physical.FullDuplexUnreliablePortConfig{
			RecvUDPEndpoint: ":50001",
			SendUDPEndpoint: ":50002",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, port1)
	port1Recv := port1.Recv()

	port2MAC, err := net.ParseMAC("00:00:5e:00:53:af")
	require.NoError(t, err)
	port2, err := link.NewEthernetPort(context.Background(), link.EthernetPortConfig{
		ForwardingMode: true,
		MACAddress:     port2MAC.String(),
		Medium: &physical.FullDuplexUnreliablePortConfig{
			RecvUDPEndpoint: ":50002",
			SendUDPEndpoint: ":50001",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, port2)
	port2Recv := port2.Recv()

	port1Payload := []byte("hello port2")
	require.NoError(t, port1.Send(context.Background(), &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: port1Payload,
		},
		DstMAC:       port2MAC,
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(port1Payload)),
	}))

	assertFrame(t, port2Recv, port1MAC, port2MAC, port1Payload)

	assert.NoError(t, port1.Close())
	assert.NoError(t, port2.Close())

	drainChannels(t, port1Recv)
	drainChannels(t, port2Recv)
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

func drainChannels(t *testing.T, ch <-chan *gplayers.Ethernet) {
	for eth := range ch {
		t.Errorf("received more ethernet frames than expected: %+v", eth)
	}
}
