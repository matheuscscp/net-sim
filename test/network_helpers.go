package test

import (
	"context"
	"net"
	"runtime/debug"
	"testing"
	"time"

	"github.com/matheuscscp/net-sim/layers/network"

	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func AssertDatagram(
	t *testing.T,
	ch <-chan *gplayers.IPv4,
	src, dst net.IP,
	payload []byte,
) {
	expectedBuf, err := network.SerializeDatagram(&gplayers.IPv4{
		BaseLayer: gplayers.BaseLayer{
			Payload: payload,
		},
		SrcIP:   src,
		DstIP:   dst,
		Version: 4,
		IHL:     5,
		Length:  uint16(len(payload)) + 20,
	})
	require.NoError(t, err)
	expected, err := network.DeserializeDatagram(expectedBuf)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var actual *gplayers.IPv4
	select {
	case <-ctx.Done():
		t.Error("timeout reading from channel.", string(debug.Stack()))
	case actual = <-ch:
	}

	assert.Equal(t, expected, actual)
}

func FlagErrorForUnexpectedDatagrams(t *testing.T, ch <-chan *gplayers.IPv4) {
	for datagram := range ch {
		t.Errorf("received more ip datagrams than expected: %+v", datagram)
	}
}

func CloseIntfsAndFlagErrorForUnexpectedData(t *testing.T, intfs ...network.Interface) {
	for _, intf := range intfs {
		assert.NoError(t, intf.Close())
		FlagErrorForUnexpectedDatagrams(t, intf.Recv())
		CloseEthPortsAndFlagErrorForUnexpectedData(t, intf.Card())
	}
}

func MustParseCIDR(t *testing.T, s string) *net.IPNet {
	_, a, err := net.ParseCIDR(s)
	require.NoError(t, err)
	return a
}
