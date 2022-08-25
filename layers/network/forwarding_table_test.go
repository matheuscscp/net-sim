package network_test

import (
	"net"
	"testing"

	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForwardingTable(t *testing.T) {
	t.Parallel()

	table, err := network.NewForwardingTable(network.ForwardingTableConfig{
		DefaultRoute: network.RouteConfig{Interface: "eth0"},
		Routes: []network.RouteConfig{
			{
				NetworkCIDR: "1.1.0.0/16",
				Interface:   "eth1",
			},
			{
				NetworkCIDR: "1.1.1.0/24",
				Interface:   "eth2",
			},
			{
				NetworkCIDR: "1.1.2.0/24",
				Interface:   "eth3",
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, table)

	for name, tt := range map[string]*struct {
		ipv4 string
		intf string
	}{
		"default-route": {
			ipv4: "1.123.1.1",
			intf: "eth0",
		},
		"wide-route": {
			ipv4: "1.1.123.1",
			intf: "eth1",
		},
		"specific-route-1": {
			ipv4: "1.1.1.1",
			intf: "eth2",
		},
		"specific-route-2": {
			ipv4: "1.1.2.1",
			intf: "eth3",
		},
	} {
		t.Run(name, func(t *testing.T) {
			tt := tt // copy for running in parallel
			t.Parallel()

			assert.Equal(t, tt.intf, table.FindRoute(net.ParseIP(tt.ipv4)))
		})
	}
}

func TestForwardingTableSetDefaultRoute(t *testing.T) {
	t.Parallel()

	table := &network.ForwardingTable{}
	table.SetDefaultRoute("eth0")
	assert.Equal(t, "eth0", table.FindRoute(net.ParseIP("1.1.1.1")))
}

func TestForwardingTableStoreRoute(t *testing.T) {
	t.Parallel()

	table := &network.ForwardingTable{}

	err := table.StoreRoute(test.MustParseCIDR(t, "1.1.1.0/24"), "eth0")
	require.NoError(t, err)
	assert.Equal(t, "eth0", table.FindRoute(net.ParseIP("1.1.1.1")))

	err = table.StoreRoute(test.MustParseCIDR(t, "1.1.1.0/24"), "eth1")
	require.NoError(t, err)
	assert.Equal(t, "eth1", table.FindRoute(net.ParseIP("1.1.1.1")))
}

func TestForwardingTableDeleteRoute(t *testing.T) {
	t.Parallel()

	table := &network.ForwardingTable{}
	table.SetDefaultRoute("eth0")

	err := table.StoreRoute(test.MustParseCIDR(t, "1.1.1.0/24"), "eth1")
	require.NoError(t, err)
	assert.Equal(t, "eth1", table.FindRoute(net.ParseIP("1.1.1.1")))

	table.DeleteRoute(test.MustParseCIDR(t, "1.1.1.0/24"))
	assert.Equal(t, "eth0", table.FindRoute(net.ParseIP("1.1.1.1")))
}
