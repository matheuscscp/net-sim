package transport

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/hashicorp/go-multierror"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/sirupsen/logrus"
)

type (
	listenerSet struct {
		transportLayer  *layer
		protocolFactory protocolFactory

		listenersMu sync.RWMutex
		listeners   map[uint16]*listener
	}

	handshake interface {
		recv(segment gopacket.TransportLayer)
		do(ctx context.Context, c conn) error
	}

	protocolFactory interface {
		newClientHandshake() handshake
		newServerHandshake() handshake
		newConn(l *listener, remoteAddr addr, h handshake) conn
		newAddr(addr addr) net.Addr
		decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error)
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
	return l.Dial(ctx, address)
}

func (s *listenerSet) decapAndDemux(datagram *gplayers.IPv4) {
	// decap
	segment, err := s.protocolFactory.decap(datagram)
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
	if s.listeners == nil {
		s.listenersMu.RUnlock()
		return
	}
	l, ok := s.listeners[dstPort]
	s.listenersMu.RUnlock()
	if !ok {
		return
	}
	if !l.matchesDstIPAddress(dstIPAddress) {
		return
	}

	// find conn and receive
	srcPort, srcIPAddress := portFromEndpoint(flow.Src()), gplayers.NewIPEndpoint(datagram.SrcIP)
	if c := l.findConnOrCreatePending(addr{srcPort, srcIPAddress}); c != nil {
		c.recv(segment)
	}
}

func (s *listenerSet) deleteListener(port uint16) {
	s.listenersMu.Lock()
	if s.listeners != nil {
		delete(s.listeners, port)
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
	var err error
	for port, lis := range listeners {
		if lErr := lis.Close(); lErr != nil {
			err = multierror.Append(err, fmt.Errorf("error closing connection to %d: %w", port, lErr))
		}
	}
	return err
}
