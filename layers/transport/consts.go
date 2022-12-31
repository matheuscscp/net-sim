package transport

import (
	"time"

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

	// TCPWindowSize is the maximum amount of bytes that TCP will send before blocking
	// and waiting for the respective ACKs.
	TCPWindowSize = (1 << 16) - 1

	// UDP is the "udp" network.
	UDP = "udp"

	// UDPHeaderLength is the UDP header length.
	UDPHeaderLength = 8

	// UDPMTU (UDP maximum transmission unit) is the maximum number of bytes that are
	// allowed on the payload of a UDP segment (the transport layer name for a packet).
	UDPMTU = network.MTU - UDPHeaderLength

	channelSize  = 1024
	demuxThreads = 16

	tcpRetransmissionTimeout = 200 * time.Millisecond
	tcpMaxReadCacheItems     = (TCPWindowSize + TCPMTU - 1) / TCPMTU

	promNamespace = "transport_layer"
)
