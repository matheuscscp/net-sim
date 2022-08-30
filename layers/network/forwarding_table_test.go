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

	table, err := network.NewForwardingTable([]network.RouteConfig{
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
	})
	require.NoError(t, err)
	require.NotNil(t, table)

	for name, tt := range map[string]*struct {
		ipv4 string
		intf string
		ok   bool
	}{
		"missing-route": {
			ipv4: "1.123.1.1",
			intf: "",
			ok:   false,
		},
		"wide-route": {
			ipv4: "1.1.123.1",
			intf: "eth1",
			ok:   true,
		},
		"specific-route-1": {
			ipv4: "1.1.1.1",
			intf: "eth2",
			ok:   true,
		},
		"specific-route-2": {
			ipv4: "1.1.2.1",
			intf: "eth3",
			ok:   true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			tt := tt // copy for running in parallel
			t.Parallel()

			intf, ok := table.FindRoute(net.ParseIP(tt.ipv4))
			assert.Equal(t, tt.intf, intf)
			assert.Equal(t, tt.ok, ok)
		})
	}
}

func TestForwardingTableStoreRoute(t *testing.T) {
	t.Parallel()

	table := &network.ForwardingTable{}

	table.StoreRoute(test.MustParseCIDR(t, "1.1.1.0/24"), "eth0")
	intf, ok := table.FindRoute(net.ParseIP("1.1.1.1"))
	assert.Equal(t, "eth0", intf)
	assert.True(t, ok)

	table.StoreRoute(test.MustParseCIDR(t, "1.1.1.0/24"), "eth1")
	intf, ok = table.FindRoute(net.ParseIP("1.1.1.1"))
	assert.Equal(t, "eth1", intf)
	assert.True(t, ok)
}

func TestForwardingTableDeleteRoute(t *testing.T) {
	t.Parallel()

	table := &network.ForwardingTable{}

	table.StoreRoute(test.MustParseCIDR(t, "1.1.1.0/24"), "eth1")
	intf, ok := table.FindRoute(net.ParseIP("1.1.1.1"))
	assert.Equal(t, "eth1", intf)
	assert.True(t, ok)

	table.DeleteRoute(test.MustParseCIDR(t, "1.1.1.0/24"))
	intf, ok = table.FindRoute(net.ParseIP("1.1.1.1"))
	assert.Equal(t, "", intf)
	assert.False(t, ok)
}

func TestForwardingTableClear(t *testing.T) {
	t.Parallel()

	table := &network.ForwardingTable{}

	table.StoreRoute(test.MustParseCIDR(t, "1.1.1.0/24"), "eth1")
	intf, ok := table.FindRoute(net.ParseIP("1.1.1.1"))
	assert.Equal(t, "eth1", intf)
	assert.True(t, ok)

	table.Clear()
	intf, ok = table.FindRoute(net.ParseIP("1.1.1.1"))
	assert.Equal(t, "", intf)
	assert.False(t, ok)
}
