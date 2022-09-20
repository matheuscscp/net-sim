package transport

import (
	"net"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

type (
	protocolFactory interface {
		newClientHandshake() handshake
		newServerHandshake() handshake
		newConn(l *listener, remoteAddr addr, h handshake) conn
		newAddr(addr addr) net.Addr
		decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error)
	}

	tcp struct{}
	udp struct{}
)

func (tcp) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return DeserializeTCPSegment(datagram)
}

func (udp) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return DeserializeUDPSegment(datagram)
}
