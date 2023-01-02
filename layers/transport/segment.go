package transport

import (
	"fmt"

	"github.com/matheuscscp/net-sim/layers/network"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

func (tcp) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return DeserializeTCPSegment(datagram)
}

func DeserializeTCPSegment(datagram *gplayers.IPv4) (*gplayers.TCP, error) {
	pkt := gopacket.NewPacket(datagram.Payload, gplayers.LayerTypeTCP, gopacket.Lazy)
	segment := pkt.TransportLayer().(*gplayers.TCP)
	if segment == nil || pkt.ErrorLayer() != nil { // a TCP segment might not have a payload
		return nil, fmt.Errorf("error deserializing tcp layer: %w", pkt.ErrorLayer().Error())
	}
	if err := validateChecksum(datagram, segment); err != nil {
		return nil, err
	}
	return segment, nil
}

func (tcp) shouldCreatePendingConn(segment gopacket.TransportLayer) bool {
	t := segment.(*gplayers.TCP)
	return t != nil && t.SYN && !t.ACK
}

func (udp) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return DeserializeUDPSegment(datagram)
}

func DeserializeUDPSegment(datagram *gplayers.IPv4) (*gplayers.UDP, error) {
	pkt := gopacket.NewPacket(datagram.Payload, gplayers.LayerTypeUDP, gopacket.Lazy)
	segment := pkt.TransportLayer().(*gplayers.UDP)
	if segment == nil || len(segment.Payload) == 0 { // a UDP segment must always have a payload
		return nil, fmt.Errorf("error deserializing udp layer: %w", pkt.ErrorLayer().Error())
	}
	return segment, nil
}

func (udp) shouldCreatePendingConn(segment gopacket.TransportLayer) bool {
	return true
}

func validateChecksum(datagram *gplayers.IPv4, segment gopacket.TransportLayer) error {
	actual := fetchChecksum(segment)
	if err := segment.(network.TCPIPSegment).SetNetworkLayerForChecksum(datagram); err != nil {
		return fmt.Errorf("error setting network layer for checksum: %w", err)
	}
	err := gopacket.SerializeLayers(
		gopacket.NewSerializeBuffer(),
		gopacket.SerializeOptions{ComputeChecksums: true},
		segment.(gopacket.SerializableLayer),
		gopacket.Payload(segment.LayerPayload()),
	)
	if err != nil {
		return fmt.Errorf("error calculating checksum (reserializing): %w", err)
	}
	if expected := fetchChecksum(segment); expected != actual {
		return fmt.Errorf("checksums differ. want %d, got %d", expected, actual)
	}
	return nil
}

func fetchChecksum(segment gopacket.TransportLayer) uint16 {
	switch s := segment.(type) {
	case *gplayers.TCP:
		return s.Checksum
	case *gplayers.UDP:
		return s.Checksum
	default:
		return 0
	}
}
