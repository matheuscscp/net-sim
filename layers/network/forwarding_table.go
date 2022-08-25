package network

import (
	"errors"
	"fmt"
	"net"
	"sync"

	pkgnet "github.com/matheuscscp/net-sim/pkg/net"
)

type (
	// ForwardingTable is a trie which is able to find a route
	// for an IP datagram (an interface name to go out from) with
	// an O(32) scan in the worst case. The dst IP address bits
	// are used to index the trie, starting from the most
	// significant bit. The scan only stops when the next trie
	// node doesn't have the next bit, hence returning the most
	// specific known route. A default route (a network interface
	// name) is returned when no routes are known for a given IP
	// address.
	ForwardingTable struct {
		root             *forwardingTableNode
		mu               sync.RWMutex
		defaultRouteIntf string // interface name
	}

	// ForwardingTableConfig contains the configs for initializing
	// a ForwardingTable.
	ForwardingTableConfig struct {
		// DefaultRoute.NetworkCIDR is ignored.
		DefaultRoute RouteConfig   `yaml:"defaultRoute"`
		Routes       []RouteConfig `yaml:"routes"`
	}

	// RouteConfig represents a route: a network CIDR mapping to an
	// interface name.
	RouteConfig struct {
		NetworkCIDR string `yaml:"networkCIDR"`
		Interface   string `yaml:"interface"`
	}

	forwardingTableNode struct {
		parent *forwardingTableNode
		zero   *forwardingTableNode
		one    *forwardingTableNode
		intf   *string
	}
)

// NewForwardingTable is a convenience constructor for a ForwardingTable.
// Directly instantiating the public struct is also valid.
func NewForwardingTable(conf ForwardingTableConfig) (*ForwardingTable, error) {
	f := &ForwardingTable{defaultRouteIntf: conf.DefaultRoute.Interface}
	for i := range conf.Routes {
		route := &conf.Routes[i]
		network, err := pkgnet.ParseNetworkCIDR(route.NetworkCIDR)
		if err != nil {
			return nil, fmt.Errorf("error parsing network cidr for route %d: %w", i, err)
		}
		ipv4, prefixLength := toIPv4AndPrefixLength(network)
		if prefixLength == 0 {
			return nil, fmt.Errorf("network prefix length of route %d is zero. prefer specifying a default route", i)
		}
		intf := route.Interface
		f.storeRoute(ipv4, prefixLength, intf)
	}
	return f, nil
}

func (f *ForwardingTable) FindRoute(ipAddress net.IP) string {
	ipv4 := ipAddress.To4()

	f.mu.RLock()
	defer f.mu.RUnlock()

	intf := &f.defaultRouteIntf
	u := f.root
	for _, octet := range ipv4 {
		for i := 7; 0 <= i; i-- {
			if u == nil {
				return *intf
			}
			if u.intf != nil {
				intf = u.intf
			}
			bit := (octet >> i) & 1
			if bit == 1 {
				u = u.one
			} else {
				u = u.zero
			}
		}
	}

	return *intf
}

func (f *ForwardingTable) SetDefaultRoute(intf string) {
	f.mu.Lock()
	f.defaultRouteIntf = intf
	f.mu.Unlock()
}

func (f *ForwardingTable) StoreRoute(network *net.IPNet, intf string) error {
	ipv4, prefixLength := toIPv4AndPrefixLength(network)
	if prefixLength == 0 {
		return errors.New("cannot store route for /0 network. prefer specifying a default route")
	}

	f.mu.Lock()
	f.storeRoute(ipv4, prefixLength, intf)
	f.mu.Unlock()

	return nil
}

func (f *ForwardingTable) storeRoute(ipv4 net.IP, prefixLength int, intf string) {
	u := f.findNode(ipv4, prefixLength, true /*create*/)
	u.intf = &intf
}

func (f *ForwardingTable) DeleteRoute(network *net.IPNet) {
	ipv4, prefixLength := toIPv4AndPrefixLength(network)

	f.mu.Lock()
	defer f.mu.Unlock()

	u := f.findNode(ipv4, prefixLength, false /*create*/)
	if u == nil {
		return
	}

	// delete route
	u.intf = nil

	// cleanup trie
	for parent := u.parent; parent != nil; parent = u.parent {
		if u.one != nil || u.zero != nil || u.intf != nil {
			return
		}
		u.parent = nil
		if parent.one == u {
			parent.one = nil
		} else {
			parent.zero = nil
		}
		u = parent
	}
	// u is the root at this point
	if f.root.one == nil && f.root.zero == nil {
		f.root = nil
	}
}

func (f *ForwardingTable) findNode(
	ipv4 net.IP,
	prefixLength int,
	create bool,
) *forwardingTableNode {
	if prefixLength == 0 {
		return nil
	}

	if f.root == nil {
		if !create {
			return nil
		}
		f.root = &forwardingTableNode{}
	}

	bitIdx := 0
	u := f.root
	for _, octet := range ipv4 {
		for i := 7; 0 <= i; i-- {
			bit := (octet >> i) & 1
			if bit == 1 {
				if u.one == nil {
					if !create {
						return nil
					}
					u.one = &forwardingTableNode{parent: u}
				}
				u = u.one
			} else {
				if u.zero == nil {
					if !create {
						return nil
					}
					u.zero = &forwardingTableNode{parent: u}
				}
				u = u.zero
			}
			bitIdx++
			if bitIdx == prefixLength {
				return u
			}
		}
	}

	return nil
}

func toIPv4AndPrefixLength(network *net.IPNet) (net.IP, int) {
	prefixLength, _ := network.Mask.Size()
	ipv4 := network.IP.To4()
	return ipv4, prefixLength
}
