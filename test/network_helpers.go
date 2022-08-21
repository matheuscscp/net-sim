package test

import (
	"net"
	"testing"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func AssertDatagram(
	t *testing.T,
	ch <-chan *gplayers.IPv4,
	src, dst net.IP,
	payloadBuf []byte,
) {
	expected := &gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: payloadBuf,
		},
		SrcIP:   src,
		DstIP:   dst,
		Version: 4,
		IHL:     5,
		Length:  uint16(len(payloadBuf)) + 20,
	}
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{}
	require.NoError(t, expected.SerializeTo(buf, opts))
	expected.Contents = gopacket.
		NewPacket(buf.Bytes(), gplayers.LayerTypeIPv4, gopacket.Lazy).
		NetworkLayer().
		LayerContents()
	actual := <-ch
	assert.Equal(t, expected, actual)
}

func FlagErrorForUnexpectedDatagrams(t *testing.T, ch <-chan *gplayers.IPv4) {
	for ip := range ch {
		t.Errorf("received more ip datagrams than expected: %+v", ip)
	}
}
