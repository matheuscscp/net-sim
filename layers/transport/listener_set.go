package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"

	gplayers "github.com/google/gopacket/layers"
	pkgio "github.com/matheuscscp/net-sim/pkg/io"
)

type (
	listenerSet struct {
		transportLayer  *layer
		protocolFactory protocolFactory

		listenersMu sync.RWMutex
		listeners   map[uint16]*listener
	}
)

func newListenerSet(transportLayer *layer, factory protocolFactory) *listenerSet {
	return &listenerSet{
		transportLayer:  transportLayer,
		protocolFactory: factory,
		listeners:       make(map[uint16]*listener),
	}
}

func (s *listenerSet) listen(ctx context.Context, address string) (*listener, error) {
	// parse address
	port, ipAddress, err := parseHostPort(address, false /*needIP*/)
	if err != nil {
		return nil, fmt.Errorf("error parsing address: %w", err)
	}

	s.listenersMu.Lock()
	defer s.listenersMu.Unlock()

	if s.listeners == nil {
		return nil, ErrProtocolClosed
	}

	// if port is zero, choose a free port
	if port == 0 {
		for p := uint16(65535); 1 <= p; p-- {
			if _, ok := s.listeners[p]; !ok {
				port = p
				break
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		if port == 0 {
			return nil, ErrAllPortsAlreadyInUse
		}
	}

	// check port already in use
	if _, ok := s.listeners[port]; ok {
		return nil, ErrPortAlreadyInUse
	}

	// allocate port
	l := newListener(s, port, ipAddress)
	s.listeners[port] = l

	return l, nil
}

func (s *listenerSet) dial(ctx context.Context, address string) (net.Conn, error) {
	// listen on a random port and stop accepting connections
	l, err := s.listen(ctx, ":0")
	if err != nil {
		return nil, fmt.Errorf("error trying to listen on a free port: %w", err)
	}
	if err := l.stopListening(); err != nil {
		return nil, fmt.Errorf("error closing local port for accepting connections: %w", err)
	}

	// then dial
	c, err := l.Dial(ctx, address)
	if err != nil {
		return nil, err
	}
	return &clientConn{c}, nil
}

func (s *listenerSet) decapAndDemux(datagram *gplayers.IPv4) error {
	// decap
	segment, err := s.protocolFactory.decap(datagram)
	if err != nil {
		return fmt.Errorf("error decapsulating transport layer: %w", err)
	}
	flow := segment.TransportFlow()

	// find listener
	dstPort, dstIPAddress := portFromEndpoint(flow.Dst()), gplayers.NewIPEndpoint(datagram.DstIP)
	s.listenersMu.RLock()
	if s.listeners == nil {
		s.listenersMu.RUnlock()
		return ErrProtocolClosed
	}
	l, ok := s.listeners[dstPort]
	s.listenersMu.RUnlock()
	if !ok || !l.matchesDstIPAddress(dstIPAddress) {
		return &ListenerNotFoundError{
			Segment: segment,
			Addr:    fmt.Sprintf("%s:%d", dstIPAddress, dstPort),
		}
	}

	// find conn and receive
	srcPort, srcIPAddress := portFromEndpoint(flow.Src()), gplayers.NewIPEndpoint(datagram.SrcIP)
	if c := l.findConnOrCreatePending(addr{srcPort, srcIPAddress}); c != nil {
		c.recv(segment)
	}

	return nil
}

func (s *listenerSet) deleteListener(l *listener) {
	s.listenersMu.Lock()
	if s.listeners != nil {
		delete(s.listeners, l.port)
	}
	s.listenersMu.Unlock()
}

func (s *listenerSet) Close() error {
	// delete the listeners map so new listen() calls fail
	var listeners map[uint16]*listener
	s.listenersMu.Lock()
	listeners, s.listeners = s.listeners, nil
	if listeners == nil {
		s.listenersMu.Unlock()
		return nil
	}
	s.listenersMu.Unlock()

	// close listeners
	closers := make([]io.Closer, 0, len(listeners))
	for _, l := range listeners {
		closers = append(closers, l)
	}
	return pkgio.Close(closers...)
}
