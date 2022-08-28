package network

import (
	"net"

	"github.com/matheuscscp/net-sim/layers/link"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

const (
	// Version is the version of the IP protocol
	Version = 4

	// IHL is the IPv4 header length in 32-bit words.
	IHL = HeaderLength / 4

	// HeaderLength is the IPv4 header length.
	HeaderLength = 20

	// MTU (maximum transmission unit) is the maximum number of bytes that are
	// allowed on the payload of a datagram (the network layer name for a packet).
	MTU = link.MTU - HeaderLength

	// MaxQueueSize is the maximum size of the network layer channels.
	MaxQueueSize = 1024
)

// LoopbackIPAddress is the IP address used for loopback in a host.
func LoopbackIPAddress() net.IP {
	return net.IPv4(127, 0, 0, 1).To4()
}

// LoopbackIPEndpoint is the IP address used for loopback in a host.
func LoopbackIPEndpoint() gopacket.Endpoint {
	return gplayers.NewIPEndpoint(LoopbackIPAddress())
}

// Internet is the the CIDR block 0.0.0.0/0.
func Internet() *net.IPNet {
	return &net.IPNet{
		IP:   net.IPv4(0, 0, 0, 0).To4(),
		Mask: net.IPv4Mask(0, 0, 0, 0),
	}
}
