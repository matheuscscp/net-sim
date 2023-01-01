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
		shouldCreatePendingConn(segment gopacket.TransportLayer) bool
	}

	tcp struct{}
	udp struct{}
)
