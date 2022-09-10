package transport

import (
	"net"

	"github.com/google/gopacket"
)

type (
	listener interface {
		net.Listener
		matchesDstIPAddress(dstIPAddress gopacket.Endpoint) bool
		demux(remoteAddr addr) conn
		findOrCreateConn(remoteAddr addr) conn
	}
)
