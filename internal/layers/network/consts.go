package network

const (
	// MTU (maximum transmission unit) is the maximum number of bytes that are
	// allowed on the payload of a datagram (the network layer name for a packet).
	MTU = 65535 - 20 // header size

	// MaxQueueSize is the maximum size of the network layer channels.
	MaxQueueSize = 1024

	// MaxARPRequests is the maximum amount of ARP requests for sending an IP
	// datagram.
	MaxARPRequests = 3
)
