package physical

const (
	// MTU (maximum transmission unit) is the maximum number of bytes that are
	// allowed on the payload of the physical layer. In reality such concept
	// does not exist. MTU is usually defined for the link layer and upwards,
	// but here we are creating a hypothetical network on top of something else,
	// which happens to be UDP in our choice. According to this reference:
	//
	// https://stackoverflow.com/a/35697810
	//
	// the practical maximum safe MTU for an IP datagram is 576 after RFCs 791
	// and 1122. Since the UDP header has 8 bytes, we are left with 568 bytes.
	// To be even safer and make it round (and exactly one third of the usual
	// 1500) we use 500.
	MTU = 500

	channelSize = 1024

	promNamespace = "physical_layer"
)
