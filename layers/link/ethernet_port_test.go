package link_test

import (
	"context"
	"net"
	"testing"

	"github.com/matheuscscp/net-sim/layers/link"
	"github.com/matheuscscp/net-sim/layers/physical"
	"github.com/matheuscscp/net-sim/test"

	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/require"
)

func TestConnectedPorts(t *testing.T) {
	port1MAC, err := net.ParseMAC("00:00:5e:00:53:ae")
	require.NoError(t, err)
	port1, err := link.NewEthernetPort(context.Background(), link.EthernetPortConfig{
		MACAddress: port1MAC.String(),
		Medium: physical.FullDuplexUnreliableWireConfig{
			RecvUDPEndpoint: ":50021",
			SendUDPEndpoint: ":50022",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, port1)
	port1Recv := port1.Recv()

	port2MAC, err := net.ParseMAC("00:00:5e:00:53:af")
	require.NoError(t, err)
	port2, err := link.NewEthernetPort(context.Background(), link.EthernetPortConfig{
		MACAddress: port2MAC.String(),
		Medium: physical.FullDuplexUnreliableWireConfig{
			RecvUDPEndpoint: ":50022",
			SendUDPEndpoint: ":50021",
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

	test.CloseEthPortsAndFlagErrorForUnexpectedData(t, port1, port2)
}

func TestWrongDstMACAddress(t *testing.T) {
	port1MAC, err := net.ParseMAC("00:00:5e:00:53:ae")
	require.NoError(t, err)
	port1, err := link.NewEthernetPort(context.Background(), link.EthernetPortConfig{
		MACAddress: port1MAC.String(),
		Medium: physical.FullDuplexUnreliableWireConfig{
			RecvUDPEndpoint: ":50031",
			SendUDPEndpoint: ":50032",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, port1)

	port2MAC, err := net.ParseMAC("00:00:5e:00:53:af")
	require.NoError(t, err)
	port2, err := link.NewEthernetPort(context.Background(), link.EthernetPortConfig{
		MACAddress: port2MAC.String(),
		Medium: physical.FullDuplexUnreliableWireConfig{
			RecvUDPEndpoint: ":50032",
			SendUDPEndpoint: ":50031",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, port2)
	port2Recv := port2.Recv()

	// if a port not running on forwarding mode receives a frame with a
	// dst MAC address not matching the port's MAC address (which also
	// does not match the broadcast MAC address), the frame is discarded.
	// here we prove that the frame is discarded by sending another
	// frame right after the discarded one which will not be discarded
	// and compare the payload
	port1DiscardedPayload := []byte("hello port2 discarded")
	require.NoError(t, port1.Send(context.Background(), &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: port1DiscardedPayload,
		},
		DstMAC:       []byte{0, 0, 0, 0, 0, 0},
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(port1DiscardedPayload)),
	}))
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

	test.CloseEthPortsAndFlagErrorForUnexpectedData(t, port1, port2)
}

func TestBroadcastMACAddress(t *testing.T) {
	port1MAC, err := net.ParseMAC("00:00:5e:00:53:ae")
	require.NoError(t, err)
	port1, err := link.NewEthernetPort(context.Background(), link.EthernetPortConfig{
		MACAddress: port1MAC.String(),
		Medium: physical.FullDuplexUnreliableWireConfig{
			RecvUDPEndpoint: ":50041",
			SendUDPEndpoint: ":50042",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, port1)

	port2MAC, err := net.ParseMAC("00:00:5e:00:53:af")
	require.NoError(t, err)
	port2, err := link.NewEthernetPort(context.Background(), link.EthernetPortConfig{
		MACAddress: port2MAC.String(),
		Medium: physical.FullDuplexUnreliableWireConfig{
			RecvUDPEndpoint: ":50042",
			SendUDPEndpoint: ":50041",
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
		DstMAC:       link.BroadcastMACAddress(),
		EthernetType: gplayers.EthernetTypeLLC,
		Length:       uint16(len(port1Payload)),
	}))

	test.AssertFrame(t, port2Recv, port1MAC, link.BroadcastMACAddress(), port1Payload)

	test.CloseEthPortsAndFlagErrorForUnexpectedData(t, port1, port2)
}

func TestForwardingMode(t *testing.T) {
	port1MAC, err := net.ParseMAC("00:00:5e:00:53:ae")
	require.NoError(t, err)
	port1, err := link.NewEthernetPort(context.Background(), link.EthernetPortConfig{
		MACAddress: port1MAC.String(),
		Medium: physical.FullDuplexUnreliableWireConfig{
			RecvUDPEndpoint: ":50051",
			SendUDPEndpoint: ":50052",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, port1)

	port2MAC, err := net.ParseMAC("00:00:5e:00:53:af")
	require.NoError(t, err)
	port2, err := link.NewEthernetPort(context.Background(), link.EthernetPortConfig{
		ForwardingMode: true,
		MACAddress:     port2MAC.String(),
		Medium: physical.FullDuplexUnreliableWireConfig{
			RecvUDPEndpoint: ":50052",
			SendUDPEndpoint: ":50051",
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

	test.CloseEthPortsAndFlagErrorForUnexpectedData(t, port1, port2)
}
