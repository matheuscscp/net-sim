package network

const (
	// Version is the version of the IP protocol
	Version = 4

	// IHL is the IPv4 header length in 32-bit words.
	IHL = HeaderLength / 4

	// HeaderLength is the IPv4 header length.
	HeaderLength = 20

	// MTU (maximum transmission unit) is the maximum number of bytes that are
	// allowed on the payload of a datagram (the network layer name for a packet).
	MTU = (1 << 16) - 1 - HeaderLength

	// MaxQueueSize is the maximum size of the network layer channels.
	MaxQueueSize = 1024
)
