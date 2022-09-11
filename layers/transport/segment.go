package transport

import (
	"fmt"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

func DeserializeUDPSegment(datagram *gplayers.IPv4) (*gplayers.UDP, error) {
	// deserialize
	pkt := gopacket.NewPacket(datagram.Payload, gplayers.LayerTypeUDP, gopacket.Lazy)
	segment := pkt.TransportLayer().(*gplayers.UDP)
	if segment == nil || len(segment.Payload) == 0 {
		return nil, fmt.Errorf("error deserializing udp layer: %w", pkt.ErrorLayer().Error())
	}

	// validate checksum
	checksum := segment.Checksum
	if err := segment.SetNetworkLayerForChecksum(datagram); err != nil {
		return nil, fmt.Errorf("error setting network layer for checksum: %w", err)
	}
	err := gopacket.SerializeLayers(
		gopacket.NewSerializeBuffer(),
		gopacket.SerializeOptions{ComputeChecksums: true},
		segment,
		gopacket.Payload(segment.Payload),
	)
	if err != nil {
		return nil, fmt.Errorf("error calculating checksum (reserializing): %w", err)
	}
	if segment.Checksum != checksum {
		return nil, fmt.Errorf("checksums differ. want %d, got %d", segment.Checksum, checksum)
	}

	return segment, nil
}
