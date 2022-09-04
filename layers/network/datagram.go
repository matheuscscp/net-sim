package network

import (
	"fmt"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

func SerializeDatagram(datagram *gplayers.IPv4) ([]byte, error) {
	setDatagramHeaderDefaultFields(datagram)
	buf := gopacket.NewSerializeBuffer()
	err := gopacket.SerializeLayers(
		buf,
		gopacket.SerializeOptions{
			FixLengths:       true,
			ComputeChecksums: true,
		},
		datagram,
		gopacket.Payload(datagram.Payload),
	)
	if err != nil {
		return nil, fmt.Errorf("error serializing network layer: %w", err)
	}
	return buf.Bytes(), nil
}

func SerializeDatagramWithTransportSegment(datagramHeader *gplayers.IPv4, segment gopacket.TransportLayer) ([]byte, error) {
	// set fields
	setDatagramHeaderDefaultFields(datagramHeader)
	err := segment.(interface {
		SetNetworkLayerForChecksum(gopacket.NetworkLayer) error
	}).SetNetworkLayerForChecksum(datagramHeader)
	if err != nil {
		return nil, fmt.Errorf("error setting network layer for checksum: %w", err)
	}

	// serialize
	buf := gopacket.NewSerializeBuffer()
	err = gopacket.SerializeLayers(
		buf,
		gopacket.SerializeOptions{
			FixLengths:       true,
			ComputeChecksums: true,
		},
		datagramHeader,
		segment.(gopacket.SerializableLayer),
		gopacket.Payload(segment.LayerPayload()),
	)
	if err != nil {
		return nil, fmt.Errorf("error serializing network and transport layers: %w", err)
	}

	return buf.Bytes(), nil
}

func DeserializeDatagram(buf []byte) (*gplayers.IPv4, error) {
	// deserialize
	pkt := gopacket.NewPacket(buf, gplayers.LayerTypeIPv4, gopacket.Lazy)
	datagram := pkt.NetworkLayer().(*gplayers.IPv4)
	if datagram == nil || len(datagram.Payload) == 0 {
		return nil, fmt.Errorf("error deserializing network layer: %w", pkt.ErrorLayer().Error())
	}

	// validate checksum
	checksum := datagram.Checksum
	err := gopacket.SerializeLayers(
		gopacket.NewSerializeBuffer(),
		gopacket.SerializeOptions{ComputeChecksums: true},
		datagram,
		gopacket.Payload(datagram.Payload),
	)
	if err != nil {
		return nil, fmt.Errorf("error calculating checksum (reserializing): %w", err)
	}
	if datagram.Checksum != checksum {
		return nil, fmt.Errorf("checksums differ. want %d, got %d", datagram.Checksum, checksum)
	}

	return datagram, nil
}

func setDatagramHeaderDefaultFields(datagramHeader *gplayers.IPv4) {
	datagramHeader.Version = Version
	datagramHeader.IHL = IHL
}
