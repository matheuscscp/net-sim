package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"

	pkgio "github.com/matheuscscp/net-sim/pkg/io"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

type (
	protocol struct {
		layer   *layer
		factory protocolFactory

		listenersMu sync.RWMutex
		listeners   map[uint16]*listener
	}

	protocolFactory interface {
		newClientHandshake() handshake
		newServerHandshake() handshake
		newConn(l *listener, remoteAddr addr, h handshake) conn
		newAddr(addr addr) net.Addr
		decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error)
		shouldCreatePendingConn(segment gopacket.TransportLayer) bool
	}

	tcpFactory struct{}
	udpFactory struct{}
)

func newProtocol(layer *layer, factory protocolFactory) *protocol {
	return &protocol{
		layer:     layer,
		factory:   factory,
		listeners: make(map[uint16]*listener),
	}
}

func (p *protocol) listen(ctx context.Context, address string) (*listener, error) {
	// parse address
	port, ipAddress, err := parseHostPort(address, false /*needIP*/)
	if err != nil {
		return nil, fmt.Errorf("error parsing address: %w", err)
	}

	p.listenersMu.Lock()
	defer p.listenersMu.Unlock()

	if p.listeners == nil {
		return nil, ErrProtocolClosed
	}

	// if port is zero, choose a free port
	if port == 0 {
		for candidate := uint16(65535); 1 <= candidate; candidate-- {
			if _, ok := p.listeners[candidate]; !ok {
				port = candidate
				break
			}
			if ctx.Err() != nil {
				return nil, fmt.Errorf("(*protocol).listen(ctx) done while choosing free port: %w", ctx.Err())
			}
		}
		if port == 0 {
			return nil, ErrAllPortsAlreadyInUse
		}
	}

	// check port already in use
	if _, ok := p.listeners[port]; ok {
		return nil, ErrPortAlreadyInUse
	}

	// allocate port
	listener := newListener(p, port, ipAddress)
	p.listeners[port] = listener

	return listener, nil
}

func (p *protocol) dial(ctx context.Context, localAddr, remoteAddr string) (net.Conn, error) {
	// listen and stop accepting connections
	listener, err := p.listen(ctx, localAddr)
	if err != nil {
		return nil, fmt.Errorf("error trying to listen on a free port: %w", err)
	}
	if err := listener.stopListening(); err != nil {
		return nil, fmt.Errorf("error stopping client port from listening to incoming connections: %w", err)
	}

	// then dial
	conn, err := listener.Dial(ctx, remoteAddr)
	if err != nil {
		return nil, err
	}
	return &clientConn{conn}, nil
}

func (p *protocol) decapAndDemux(datagram *gplayers.IPv4) error {
	// decap
	segment, err := p.factory.decap(datagram)
	if err != nil {
		return fmt.Errorf("error decapsulating transport layer: %w", err)
	}
	flow := segment.TransportFlow()

	// find listener
	dstPort, dstIPAddress := portFromEndpoint(flow.Dst()), gplayers.NewIPEndpoint(datagram.DstIP)
	localAddr := addr{dstPort, dstIPAddress}
	p.listenersMu.RLock()
	if p.listeners == nil {
		p.listenersMu.RUnlock()
		return ErrProtocolClosed
	}
	listener, ok := p.listeners[dstPort]
	p.listenersMu.RUnlock()
	if !ok || !listener.matchesDstIPAddress(dstIPAddress) {
		return &listenerNotFoundError{
			segment: segment,
			addr:    localAddr.String(),
		}
	}

	// find conn and receive
	srcPort, srcIPAddress := portFromEndpoint(flow.Src()), gplayers.NewIPEndpoint(datagram.SrcIP)
	remoteAddr := addr{srcPort, srcIPAddress}
	if conn := listener.findConnOrCreatePending(remoteAddr, segment); conn != nil {
		conn.recv(segment)
	} else {
		return &connNotFoundError{
			segment:    segment,
			localAddr:  localAddr.String(),
			remoteAddr: remoteAddr.String(),
		}
	}

	return nil
}

func (p *protocol) deleteListener(l *listener) {
	p.listenersMu.Lock()
	if p.listeners != nil {
		delete(p.listeners, l.port)
	}
	p.listenersMu.Unlock()
}

func (p *protocol) Close() error {
	// delete the listeners map so new listen() calls fail
	var listeners map[uint16]*listener
	p.listenersMu.Lock()
	listeners, p.listeners = p.listeners, nil
	if listeners == nil {
		p.listenersMu.Unlock()
		return nil
	}
	p.listenersMu.Unlock()

	// close listeners
	closers := make([]io.Closer, 0, len(listeners))
	for _, l := range listeners {
		closers = append(closers, l)
	}
	return pkgio.Close(closers...)
}
