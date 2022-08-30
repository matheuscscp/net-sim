package network

import (
	"net"
	"sync"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

type (
	// ARPTable represents an ARP table, which maps IP addresses
	// to MAC addresses in the local network. All the public
	// methods are thread-safe.
	ARPTable struct {
		mp map[gopacket.Endpoint]gopacket.Endpoint
		mu sync.RWMutex
	}
)

func (a *ARPTable) FindRoute(ipAddress gopacket.Endpoint) (gopacket.Endpoint, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.mp == nil {
		return gopacket.Endpoint{}, false
	}

	macAddress, ok := a.mp[ipAddress]
	return macAddress, ok
}

func (a *ARPTable) StoreRoute(ipAddress net.IP, macAddress net.HardwareAddr) {
	k := gplayers.NewIPEndpoint(ipAddress)
	v := gplayers.NewMACEndpoint(macAddress)

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.mp == nil {
		a.mp = make(map[gopacket.Endpoint]gopacket.Endpoint)
	}
	a.mp[k] = v
}
