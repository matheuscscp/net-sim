package transport

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
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

func parseHostPort(address string, needIP bool) (uint16, *gopacket.Endpoint, error) {
	host, p, err := net.SplitHostPort(address)
	if err != nil {
		return 0, nil, fmt.Errorf("error splitting in host:port: %w", err)
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return 0, nil, fmt.Errorf("error parsing port number: %w", err)
	}
	if port < 0 || 65535 < port {
		return 0, nil, errors.New("port number must be in range [0, 65535]")
	}
	var ipAddress *gopacket.Endpoint
	if len(host) > 0 {
		ip := net.ParseIP(host)
		if ip == nil {
			return 0, nil, fmt.Errorf("unknown error parsing host '%s' as IP address", host)
		}
		ep := gplayers.NewIPEndpoint(ip)
		ipAddress = &ep
	}
	if needIP && ipAddress == nil {
		return 0, nil, errors.New("host cannot be empty")
	}
	return uint16(port), ipAddress, nil
}

func portFromEndpoint(ep gopacket.Endpoint) uint16 {
	return binary.BigEndian.Uint16(ep.Raw())
}
