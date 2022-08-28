package network

import (
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
	// specific known route.
	ForwardingTable struct {
		root *forwardingTableNode
		mu   sync.RWMutex
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
func NewForwardingTable(routes []RouteConfig) (*ForwardingTable, error) {
	f := &ForwardingTable{}
	for i := range routes {
		route := &routes[i]
		network, err := pkgnet.ParseNetworkCIDR(route.NetworkCIDR)
		if err != nil {
			return nil, fmt.Errorf("error parsing network cidr for route %d: %w", i, err)
		}
		ipv4, prefixLength := toIPv4AndPrefixLength(network)
		intf := route.Interface
		f.storeRoute(ipv4, prefixLength, intf)
	}
	return f, nil
}

func (f *ForwardingTable) FindRoute(ipAddress net.IP) (intf string, ok bool) {
	ipv4 := ipAddress.To4()

	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.root == nil {
		return
	}
	u := f.root
	if u.intf != nil {
		intf, ok = *u.intf, true
	}
	for _, octet := range ipv4 {
		for i := 7; 0 <= i; i-- {
			bit := (octet >> i) & 1
			if bit == 1 {
				u = u.one
			} else {
				u = u.zero
			}
			if u == nil {
				return
			}
			if u.intf != nil {
				intf, ok = *u.intf, true
			}
		}
	}

	return
}

// StoreRoutesFromConfig atomically stores a list of routes if all of them
// are valid.
func (f *ForwardingTable) StoreRoutesFromConfig(routes []RouteConfig) error {
	networks := make([]*net.IPNet, 0, len(routes))
	for i := range routes {
		network, err := pkgnet.ParseNetworkCIDR(routes[i].NetworkCIDR)
		if err != nil {
			return fmt.Errorf("error parsing network cidr for route %d: %w", i, err)
		}
		networks = append(networks, network)
	}
	for i := range routes {
		ipv4, prefixLength := toIPv4AndPrefixLength(networks[i])
		f.mu.Lock()
		f.storeRoute(ipv4, prefixLength, routes[i].Interface)
		f.mu.Unlock()
	}
	return nil
}

func (f *ForwardingTable) StoreRoute(network *net.IPNet, intf string) {
	ipv4, prefixLength := toIPv4AndPrefixLength(network)
	f.mu.Lock()
	f.storeRoute(ipv4, prefixLength, intf)
	f.mu.Unlock()
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
	for ; u != nil; u = u.parent {
		// are there still relevant routes under u?
		if u.one != nil || u.zero != nil || u.intf != nil {
			return
		}
		// no, u can be reaped

		if u.parent == nil { // u is the root
			break
		}

		// cleanup bidirectional edge between u and u.parent
		parent := u.parent
		u.parent = nil
		if parent.one == u {
			parent.one = nil
		} else {
			parent.zero = nil
		}
	}
	// u is the root at this point
	f.root = nil
}

func (f *ForwardingTable) findNode(
	ipv4 net.IP,
	prefixLength int,
	create bool,
) *forwardingTableNode {
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
			if bitIdx == prefixLength {
				return u
			}
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
		}
	}

	return u
}

func toIPv4AndPrefixLength(network *net.IPNet) (net.IP, int) {
	prefixLength, _ := network.Mask.Size()
	ipv4 := network.IP.To4()
	return ipv4, prefixLength
}
