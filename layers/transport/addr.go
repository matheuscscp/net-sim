package transport

import (
	"fmt"

	"github.com/google/gopacket"
)

type (
	addr struct {
		port      uint16
		ipAddress gopacket.Endpoint
	}
)

func (a *addr) Network() string {
	return UDP
}

func (a *addr) String() string {
	return fmt.Sprintf("%s:%d", a.ipAddress, a.port)
}
