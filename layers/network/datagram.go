package network

import (
	"fmt"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

type (
	TCPIPSegment interface {
		gopacket.TransportLayer
		SetNetworkLayerForChecksum(l gopacket.NetworkLayer) error
	}
)

func SerializeDatagram(datagram *gplayers.IPv4) ([]byte, error) {
	setDatagramHeaderDefaultFields(datagram)
	b, err := serializeLayers(
		datagram,
		gopacket.Payload(datagram.Payload),
	)
	if err != nil {
		return nil, fmt.Errorf("error serializing network layer: %w", err)
	}
	return b, nil
}

func SerializeDatagramWithTransportSegment(datagramHeader *gplayers.IPv4, segment gopacket.TransportLayer) ([]byte, error) {
	setDatagramHeaderDefaultFields(datagramHeader)
	err := segment.(TCPIPSegment).SetNetworkLayerForChecksum(datagramHeader)
	if err != nil {
		return nil, fmt.Errorf("error setting network layer for checksum: %w", err)
	}
	b, err := serializeLayers(
		datagramHeader,
		segment.(gopacket.SerializableLayer),
		gopacket.Payload(segment.LayerPayload()),
	)
	if err != nil {
		return nil, fmt.Errorf("error serializing network and transport layers: %w", err)
	}
	return b, nil
}

func DeserializeDatagram(buf []byte) (*gplayers.IPv4, error) {
	// deserialize
	pkt := gopacket.NewPacket(buf, gplayers.LayerTypeIPv4, gopacket.Lazy)
	datagram := pkt.NetworkLayer().(*gplayers.IPv4)
	if datagram == nil || len(datagram.Payload) == 0 { // an IP datagram must always have a payload
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

func serializeLayers(layers ...gopacket.SerializableLayer) ([]byte, error) {
	buf := gopacket.NewSerializeBuffer()
	err := gopacket.SerializeLayers(
		buf,
		gopacket.SerializeOptions{
			FixLengths:       true,
			ComputeChecksums: true,
		},
		layers...,
	)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
