package transport

import (
	"fmt"

	"github.com/matheuscscp/net-sim/layers/network"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

func (tcpFactory) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return DeserializeTCPSegment(datagram)
}

func DeserializeTCPSegment(datagram *gplayers.IPv4) (*gplayers.TCP, error) {
	pkt := gopacket.NewPacket(datagram.Payload, gplayers.LayerTypeTCP, gopacket.Lazy)
	segment, ok := pkt.TransportLayer().(*gplayers.TCP)
	if !ok || segment == nil || pkt.ErrorLayer() != nil { // a TCP segment might not have a payload
		return nil, fmt.Errorf("error deserializing tcp layer: %w", pkt.ErrorLayer().Error())
	}
	if err := validateChecksum(datagram, segment); err != nil {
		return nil, fmt.Errorf("error validating tcpip checksum: %w", err)
	}
	return segment, nil
}

func (tcpFactory) shouldCreatePendingConn(segment gopacket.TransportLayer) bool {
	t := segment.(*gplayers.TCP)
	return t != nil && t.SYN && !t.ACK
}

func (udpFactory) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return DeserializeUDPSegment(datagram)
}

func DeserializeUDPSegment(datagram *gplayers.IPv4) (*gplayers.UDP, error) {
	pkt := gopacket.NewPacket(datagram.Payload, gplayers.LayerTypeUDP, gopacket.Lazy)
	segment, ok := pkt.TransportLayer().(*gplayers.UDP)
	if !ok || segment == nil || len(segment.Payload) == 0 { // a UDP segment must always have a payload
		return nil, fmt.Errorf("error deserializing udp layer: %w", pkt.ErrorLayer().Error())
	}
	return segment, nil
}

func (udpFactory) shouldCreatePendingConn(segment gopacket.TransportLayer) bool {
	return true
}

func validateChecksum(datagram *gplayers.IPv4, segment gopacket.TransportLayer) error {
	actual := getChecksum(segment)
	tcpipSegment, ok := segment.(network.TCPIPSegment)
	if !ok || tcpipSegment == nil {
		return fmt.Errorf("segment is not a tcpip or is nil: %v", segment.TransportFlow())
	}
	if err := tcpipSegment.SetNetworkLayerForChecksum(datagram); err != nil {
		return fmt.Errorf("error setting network layer for checksum: %w", err)
	}
	err := gopacket.SerializeLayers(
		gopacket.NewSerializeBuffer(),
		gopacket.SerializeOptions{ComputeChecksums: true},
		tcpipSegment,
		gopacket.Payload(tcpipSegment.LayerPayload()),
	)
	if err != nil {
		return fmt.Errorf("error calculating checksum (reserializing): %w", err)
	}
	if expected := getChecksum(segment); expected != actual {
		return fmt.Errorf("checksums differ. want %d, got %d", expected, actual)
	}
	return nil
}

func getChecksum(segment gopacket.TransportLayer) uint16 {
	switch s := segment.(type) {
	case *gplayers.TCP:
		return s.Checksum
	case *gplayers.UDP:
		return s.Checksum
	default:
		return 0
	}
}
