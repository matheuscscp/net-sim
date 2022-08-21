package link_test

import (
	"net"
	"testing"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func flagErrorForUnexpectedFrames(t *testing.T, ch <-chan *gplayers.Ethernet) {
	for eth := range ch {
		t.Errorf("received more ethernet frames than expected: %+v", eth)
	}
}

func mustParseMAC(t *testing.T, s string) net.HardwareAddr {
	a, err := net.ParseMAC(s)
	require.NoError(t, err)
	return a
}
