package transport

import (
	"github.com/matheuscscp/net-sim/layers/network"
)

const (
	// TCP is the "tcp" network.
	TCP = "tcp"

	// TCPHeaderLength is the TCP header length.
	TCPHeaderLength = 20

	// TCPMTU (TCP maximum transmission unit) is the maximum number of bytes that are
	// allowed on the payload of a TCP segment (the transport layer name for a packet).
	TCPMTU = network.MTU - TCPHeaderLength

	// UDP is the "udp" network.
	UDP = "udp"

	// UDPHeaderLength is the UDP header length.
	UDPHeaderLength = 8

	// UDPMTU (UDP maximum transmission unit) is the maximum number of bytes that are
	// allowed on the payload of a UDP segment (the transport layer name for a packet).
	UDPMTU = network.MTU - UDPHeaderLength

	channelSize  = 1024
	demuxThreads = 16
)
