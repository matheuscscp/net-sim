package test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/matheuscscp/net-sim/layers/link"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func AssertFrame(
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
	expected = gopacket.
		NewPacket(buf.Bytes(), gplayers.LayerTypeEthernet, gopacket.Lazy).
		LinkLayer().(*gplayers.Ethernet)
	expected.Payload = payload

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var actual *gplayers.Ethernet
	select {
	case <-ctx.Done():
		t.Log("timeout reading from channel")
		t.FailNow()
	case actual = <-ch:
	}

	assert.Equal(t, expected, actual)
}

func FlagErrorForUnexpectedFrames(t *testing.T, ch <-chan *gplayers.Ethernet) {
	for frame := range ch {
		t.Errorf("received more ethernet frames than expected: %+v", frame)
	}
}

func CloseEthPortsAndFlagErrorForUnexpectedData(t *testing.T, cards ...link.EthernetPort) {
	for _, card := range cards {
		if card == nil {
			continue
		}
		assert.NoError(t, card.Close())
		FlagErrorForUnexpectedFrames(t, card.Recv())
	}
}

func MustParseMAC(t *testing.T, s string) net.HardwareAddr {
	a, err := net.ParseMAC(s)
	require.NoError(t, err)
	return a
}
