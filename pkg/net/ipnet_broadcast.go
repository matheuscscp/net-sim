package pkgnet

import (
	"net"
)

// Broadcast returns the broadcast IP address for the given
// network.
func Broadcast(ipNet *net.IPNet) net.IP {
	neg := make(net.IP, len(ipNet.Mask))
	for i := range neg {
		neg[i] = ^ipNet.Mask[i]
	}
	b := make(net.IP, len(ipNet.IP))
	for i := range b {
		b[i] = ipNet.IP[i] | neg[i]
	}
	return b
}
