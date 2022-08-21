package pkgnet

import (
	"fmt"
	"net"

	gplayers "github.com/google/gopacket/layers"
)

// ParseNetworkCIDR uses net.ParseCIDR() but returns an error if
// the returned IP address is not equal the network IP address.
func ParseNetworkCIDR(s string) (*net.IPNet, error) {
	ip, netIP, err := net.ParseCIDR(s)
	if err != nil {
		return nil, err
	}
	if gplayers.NewIPEndpoint(ip) != gplayers.NewIPEndpoint(netIP.IP) {
		return nil, fmt.Errorf("the IP address does not match the network IP address. want %s, got %s", netIP.IP, ip)
	}
	return netIP, nil
}
