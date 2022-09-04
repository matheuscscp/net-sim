package test

import (
	"context"
	"net"
	"runtime/debug"
	"testing"
	"time"

	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/transport"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func AssertUDPSegment(
	t *testing.T,
	ch <-chan gopacket.TransportLayer,
	srcPort, dstPort gplayers.UDPPort,
	srcIPAddress, dstIPAddress net.IP,
	payload []byte,
) {
	buf, err := network.SerializeDatagramWithTransportSegment(&gplayers.IPv4{
		SrcIP:    srcIPAddress,
		DstIP:    dstIPAddress,
		Protocol: gplayers.IPProtocolUDP,
	}, &gplayers.UDP{
		BaseLayer: gplayers.BaseLayer{
			Payload: payload,
		},
		SrcPort: srcPort,
		DstPort: dstPort,
		Length:  uint16(len(payload)) + 8,
	})
	require.NoError(t, err)
	require.NotEmpty(t, buf)

	datagram, err := network.DeserializeDatagram(buf)
	require.NoError(t, err)
	require.NotNil(t, datagram)

	expected, err := transport.DeserializeUDPSegment(datagram)
	require.NoError(t, err)
	require.NotNil(t, expected)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var actual gopacket.TransportLayer
	select {
	case <-ctx.Done():
		t.Error("timeout reading from channel.", string(debug.Stack()))
	case actual = <-ch:
	}

	assert.Equal(t, expected, actual)
}

func RecvUDPSegment(
	t *testing.T,
	recvdDatagrams chan<- *gplayers.IPv4,
	segment *gplayers.UDP,
	srcIPAddress, dstIPAddress net.IP,
) {
	buf, err := network.SerializeDatagramWithTransportSegment(&gplayers.IPv4{
		SrcIP:    srcIPAddress,
		DstIP:    dstIPAddress,
		Protocol: gplayers.IPProtocolUDP,
	}, segment)
	require.NoError(t, err)
	require.NotEmpty(t, buf)
	datagram, err := network.DeserializeDatagram(buf)
	require.NoError(t, err)
	require.NotNil(t, datagram)
	recvdDatagrams <- datagram
}
