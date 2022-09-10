package transport

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/matheuscscp/net-sim/layers/network"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/sirupsen/logrus"
)

type (
	listenerSet struct {
		ctx          context.Context
		networkLayer network.Layer
		factory      protocolFactory

		listenersMu sync.RWMutex
		listeners   map[uint16]*listener
	}

	protocolFactory interface {
		decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error)
		newConn(l *listener, remoteAddr addr) conn
	}
)

func newListenerSet(
	ctx context.Context,
	networkLayer network.Layer,
	factory protocolFactory,
) *listenerSet {
	return &listenerSet{
		ctx:          ctx,
		networkLayer: networkLayer,
		factory:      factory,

		listeners: make(map[uint16]*listener),
	}
}

func (s *listenerSet) listen(address string) (*listener, error) {
	// parse address
	port, ipAddress, err := parseHostPort(address, false /*needIP*/)
	if err != nil {
		return nil, fmt.Errorf("error parsing address: %w", err)
	}

	s.listenersMu.Lock()
	defer s.listenersMu.Unlock()

	// if port is zero, choose a free port
	if port == 0 {
		for p := uint16(65535); 1 <= p; p-- {
			if _, ok := s.listeners[p]; !ok {
				port = p
				break
			}
		}
		if port == 0 {
			return nil, ErrAllPortsAlreadyInUse
		}
	}

	if _, ok := s.listeners[port]; ok {
		return nil, ErrPortAlreadyInUse
	}

	l := newListener(s.ctx, s.networkLayer, s.factory, port, ipAddress)
	s.listeners[port] = l

	return l, nil
}

func (s *listenerSet) dial(ctx context.Context, address string) (net.Conn, error) {
	// listen on a random port and stop accepting connections
	l, err := s.listen(":0")
	if err != nil {
		return nil, fmt.Errorf("error trying to listen on a free port: %w", err)
	}
	if err := l.Close(); err != nil {
		return nil, fmt.Errorf("error closing local listener for other connections: %w", err)
	}

	// then dial
	return l.Dial(ctx, address)
}

func (s *listenerSet) decapAndDemux(datagram *gplayers.IPv4) {
	// decap
	segment, err := s.factory.decap(datagram)
	if err != nil {
		logrus.
			WithError(err).
			WithField("datagram", datagram).
			Error("error decapsulating transport layer")
		return
	}
	flow := segment.TransportFlow()

	// find listener
	dstPort, dstIPAddress := portFromEndpoint(flow.Dst()), gplayers.NewIPEndpoint(datagram.DstIP)
	s.listenersMu.RLock()
	l, ok := s.listeners[dstPort]
	s.listenersMu.RUnlock()
	if !ok {
		return // drop rule: port is not listening
	}
	if !l.matchesDstIPAddress(dstIPAddress) {
		return // drop rule: port is listening but dst IP address does not match the IP bound by the port
	}

	// find conn and receive
	srcPort, srcIPAddress := portFromEndpoint(flow.Src()), gplayers.NewIPEndpoint(datagram.SrcIP)
	if c := l.findConnOrCreatePending(addr{srcPort, srcIPAddress}); c != nil {
		c.recv(segment)
	}
}
