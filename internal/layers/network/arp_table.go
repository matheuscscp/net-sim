package network

import (
	"net"
	"sync"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

type (
	// ARPTable represents an ARP table, which maps IP addresses
	// to MAC address in the local network.
	ARPTable interface {
		Load(ipAddress gopacket.Endpoint) (gopacket.Endpoint, bool)
		Store(ipAddress net.IP, macAddress net.HardwareAddr)
	}

	arpTable struct {
		mp map[gopacket.Endpoint]gopacket.Endpoint
		mu sync.RWMutex
	}
)

// NewARPTable creates an ARPTable.
func NewARPTable() ARPTable {
	return &arpTable{mp: make(map[gopacket.Endpoint]gopacket.Endpoint)}
}

func (a *arpTable) Load(ipAddress gopacket.Endpoint) (gopacket.Endpoint, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	macAddress, ok := a.mp[ipAddress]
	return macAddress, ok
}

func (a *arpTable) Store(ipAddress net.IP, macAddress net.HardwareAddr) {
	k := gplayers.NewIPEndpoint(ipAddress)
	v := gplayers.NewMACEndpoint(macAddress)
	a.mu.Lock()
	a.mp[k] = v
	a.mu.Unlock()
}
