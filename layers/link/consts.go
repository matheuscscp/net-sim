package link

import (
	"net"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

const (
	// MTU (maximum transmission unit) is the maximum number of bytes that are
	// allowed on the payload of a frame (the link layer name for a packet).
	MTU = 1500

	// MaxQueueSize is the maximum size of the link layer channels.
	MaxQueueSize = 1024
)

// BroadcastMACAddress is the IP address used for broadcast in a local network.
func BroadcastMACAddress() net.HardwareAddr {
	return net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
}

// BroadcastMACEndpoint is the IP address used for broadcast in a local network.
func BroadcastMACEndpoint() gopacket.Endpoint {
	return gplayers.NewMACEndpoint(BroadcastMACAddress())
}
