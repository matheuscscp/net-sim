package link_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/matheuscscp/net-sim/layers/link"
	"github.com/matheuscscp/net-sim/layers/physical"
	"github.com/matheuscscp/net-sim/test"

	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectedPorts(t *testing.T) {
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

	test.AssertFrame(t, port1Recv, port2MAC, port1MAC, port2Payload)
	test.AssertFrame(t, port2Recv, port1MAC, port2MAC, port1Payload)

	assert.NoError(t, port1.Close())
	assert.NoError(t, port2.Close())

	test.FlagErrorForUnexpectedFrames(t, port1Recv)
	test.FlagErrorForUnexpectedFrames(t, port2Recv)
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
		DstMAC:       []byte{0, 0, 0, 0, 0, 0},
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(port1Payload)),
	}))

	time.Sleep(100 * time.Millisecond) // give time for frame to arrive and be discarded
	assert.NoError(t, port1.Close())
	assert.NoError(t, port2.Close())

	test.FlagErrorForUnexpectedFrames(t, port1Recv)
	test.FlagErrorForUnexpectedFrames(t, port2Recv)
}

func TestBroadcastMACAddress(t *testing.T) {
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
		DstMAC:       link.BroadcastMACAddress,
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(port1Payload)),
	}))

	test.AssertFrame(t, port2Recv, port1MAC, link.BroadcastMACAddress, port1Payload)

	assert.NoError(t, port1.Close())
	assert.NoError(t, port2.Close())

	test.FlagErrorForUnexpectedFrames(t, port1Recv)
	test.FlagErrorForUnexpectedFrames(t, port2Recv)
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

	test.AssertFrame(t, port2Recv, port1MAC, port2MAC, port1Payload)

	assert.NoError(t, port1.Close())
	assert.NoError(t, port2.Close())

	test.FlagErrorForUnexpectedFrames(t, port1Recv)
	test.FlagErrorForUnexpectedFrames(t, port2Recv)
}
