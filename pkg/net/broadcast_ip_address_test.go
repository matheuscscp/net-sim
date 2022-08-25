package pkgnet_test

import (
	"testing"

	pkgnet "github.com/matheuscscp/net-sim/pkg/net"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBroadcastIPAddress(t *testing.T) {
	t.Parallel()

	for network, broadcast := range map[string]string{
		"1.1.1.0/24": "1.1.1.255",
		"1.1.1.0/28": "1.1.1.15",
		"1.1.1.0/32": "1.1.1.0",
	} {
		t.Run(network, func(t *testing.T) {
			network, broadcast := network, broadcast // copy for running in parallel
			t.Parallel()

			ipnet, err := pkgnet.ParseNetworkCIDR(network)
			require.NoError(t, err)
			assert.Equal(t, broadcast, pkgnet.BroadcastIPAddress(ipnet).String())
		})
	}
}
