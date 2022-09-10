package transport

import (
	"context"
	"fmt"
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
		listeners   map[uint16]listener
	}

	protocolFactory interface {
		newListener(
			ctx context.Context,
			networkLayer network.Layer,
			port uint16,
			ipAddress *gopacket.Endpoint,
		) listener
		decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error)
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

		listeners: make(map[uint16]listener),
	}
}

func (s *listenerSet) listen(address string) (listener, error) {
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

	l := s.factory.newListener(s.ctx, s.networkLayer, port, ipAddress)
	s.listeners[port] = l

	return l, nil
}

func (s *listenerSet) dial(ctx context.Context, address string) (conn, error) {
	// parse remote address
	port, ipAddress, err := parseHostPort(address, true /*needIP*/)
	if err != nil {
		return nil, fmt.Errorf("error parsing address: %w", err)
	}

	// allocate random port
	l, err := s.listen(":0")
	if err != nil {
		return nil, fmt.Errorf("error trying to listen on a free port: %w", err)
	}

	// stop accepting connections
	if err := l.Close(); err != nil {
		return nil, fmt.Errorf("error closing local listener for other connections: %w", err)
	}

	// create local conn and dial protocol
	c := l.findOrCreateConn(addr{port, *ipAddress})
	if err := c.protocolHandshake(ctx); err != nil {
		return nil, fmt.Errorf("error performing protocol handshake: %w", err)
	}

	return c, nil
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

	// demux
	srcPort, srcIPAddress := portFromEndpoint(flow.Src()), gplayers.NewIPEndpoint(datagram.SrcIP)
	remoteAddr := addr{srcPort, srcIPAddress}
	if c := l.demux(remoteAddr); c != nil {
		c.pushRead(segment.LayerPayload())
	}
}
