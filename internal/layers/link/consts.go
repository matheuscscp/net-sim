package link

import (
	"net"

	gplayers "github.com/google/gopacket/layers"
)

const (
	// MTU (maximum transmission unit) is the maximum number of bytes that are
	// allowed on the payload of a frame (the link layer name for a packet).
	MTU = 1500

	// MaxQueueSize is the maximum size of the link layer channels.
	MaxQueueSize = 1024
)

var (
	// BroadcastMACAddress is the address used for broadcast in a local network.
	BroadcastMACAddress = net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

	// BroadcastMACEndpoint is the address used for broadcast in a local network.
	BroadcastMACEndpoint = gplayers.NewMACEndpoint(BroadcastMACAddress)
)
