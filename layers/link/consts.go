package link

import (
	"net"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/matheuscscp/net-sim/layers/physical"
)

const (
	// HeaderLength is the Ethernet header length.
	HeaderLength = 14

	// ChecksumLength is the frame check sequence (FCS) length (32-bit CRC).
	ChecksumLength = 4

	// MTU (maximum transmission unit) is the maximum number of bytes that are
	// allowed on the payload of a frame (the link layer name for a packet).
	MTU = physical.MTU - HeaderLength - ChecksumLength

	channelSize = 1024
)

// BroadcastMACAddress is the IP address used for broadcast in a local network.
func BroadcastMACAddress() net.HardwareAddr {
	return net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
}

// BroadcastMACEndpoint is the IP address used for broadcast in a local network.
func BroadcastMACEndpoint() gopacket.Endpoint {
	return gplayers.NewMACEndpoint(BroadcastMACAddress())
}
