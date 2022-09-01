package test

import (
	"context"
	"net"
	"runtime/debug"
	"testing"
	"time"

	"github.com/matheuscscp/net-sim/layers/transport"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func AssertUDPSegment(
	t *testing.T,
	ch <-chan *gplayers.UDP,
	src, dst gplayers.UDPPort,
	payload []byte,
) {
	expected := &gplayers.UDP{
		BaseLayer: gplayers.BaseLayer{
			Payload: payload,
		},
		SrcPort: src,
		DstPort: dst,
		Length:  uint16(len(payload)) + 8,
	}
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{}
	require.NoError(t, expected.SerializeTo(buf, opts))
	expected = gopacket.
		NewPacket(buf.Bytes(), gplayers.LayerTypeUDP, gopacket.Lazy).
		TransportLayer().(*gplayers.UDP)
	expected.Payload = payload

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var actual *gplayers.UDP
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
	segment.Length = uint16(len(segment.Payload) + 8)
	datagramPayload, err := transport.SerializeUDPSegment(segment)
	require.NoError(t, err)
	require.NotNil(t, datagramPayload)
	recvdDatagrams <- &gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: datagramPayload,
		},
		SrcIP:    srcIPAddress,
		DstIP:    dstIPAddress,
		Protocol: gplayers.IPProtocolUDP,
	}
}
