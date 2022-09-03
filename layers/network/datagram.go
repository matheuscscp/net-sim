package network

import (
	"fmt"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

func SerializeDatagram(datagram *gplayers.IPv4) ([]byte, error) {
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{ComputeChecksums: true}
	payload := gopacket.Payload(datagram.Payload)
	if err := gopacket.SerializeLayers(buf, opts, datagram, payload); err != nil {
		return nil, fmt.Errorf("error serializing network layer: %w", err)
	}
	return buf.Bytes(), nil
}

func DeserializeDatagram(buf []byte) (*gplayers.IPv4, error) {
	pkt := gopacket.NewPacket(buf, gplayers.LayerTypeIPv4, gopacket.Lazy)
	datagram := pkt.NetworkLayer().(*gplayers.IPv4)
	if datagram == nil || len(datagram.Payload) == 0 {
		return nil, fmt.Errorf("error deserializing network layer: %w", pkt.ErrorLayer().Error())
	}
	return datagram, nil
}
