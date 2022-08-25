package pkgnet

import (
	"net"
)

// BroadcastIPAddress returns the broadcast IP address for the given
// network, which is obtained by bitwise OR'ing the network IP address
// with the negated mask, i.e. by basically turning on the mask's zero
// bits in the network IP address.
//
// Example: 1.1.1.0/24 => 1.1.1.255
func BroadcastIPAddress(ipnet *net.IPNet) net.IP {
	b := make(net.IP, len(ipnet.IP))
	for i := range b {
		b[i] = ipnet.IP[i] | (^ipnet.Mask[i])
	}
	return b
}
