package transport

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/matheuscscp/net-sim/layers/network"

	"github.com/google/gopacket"
	"github.com/hashicorp/go-multierror"
)

type (
	Dialer interface {
		Dial(ctx context.Context, address string) (net.Conn, error)
	}

	listener struct {
		ctx          context.Context
		networkLayer network.Layer
		factory      protocolFactory
		port         uint16
		ipAddress    *gopacket.Endpoint

		connsMu             sync.Mutex
		conns, pendingConns map[addr]conn
		connsCond           *sync.Cond
	}
)

var (
	_ Dialer = (*listener)(nil)
)

func newListener(
	ctx context.Context,
	networkLayer network.Layer,
	factory protocolFactory,
	port uint16,
	ipAddress *gopacket.Endpoint,
) *listener {
	l := &listener{
		ctx:          ctx,
		networkLayer: networkLayer,
		factory:      factory,
		port:         port,
		ipAddress:    ipAddress,
		conns:        make(map[addr]conn),
		pendingConns: make(map[addr]conn),
	}
	l.connsCond = sync.NewCond(&l.connsMu)
	return l
}

func (l *listener) matchesDstIPAddress(dstIPAddress gopacket.Endpoint) bool {
	return l.ipAddress == nil || *l.ipAddress == dstIPAddress
}

func (l *listener) Accept() (net.Conn, error) {
	l.connsMu.Lock()
	defer l.connsMu.Unlock()

	// wait until one of these happens: a new conn arrives,
	// or the listener is Close()d
	if l.pendingConns == nil {
		return nil, ErrListenerClosed
	}
	for len(l.pendingConns) == 0 {
		l.connsCond.Wait()
		if l.pendingConns == nil {
			return nil, ErrListenerClosed
		}
	}

	// a conn is now availble, pick the first
	for addr, conn := range l.pendingConns {
		delete(l.pendingConns, addr)
		l.conns[addr] = conn
		return conn, nil
	}
	return nil, nil
}

func (l *listener) Close() error {
	// first, delete the pendingConns map so new conns are dropped
	// as soon as possible
	l.connsMu.Lock()
	pendingConns := l.pendingConns
	l.pendingConns = nil
	l.connsCond.Broadcast()
	l.connsMu.Unlock()

	// close pending conns
	var err error
	for addr, pendingConn := range pendingConns {
		if cErr := pendingConn.Close(); cErr != nil {
			err = multierror.Append(err, fmt.Errorf("error closing pending connection from %s: %w", addr.String(), cErr))
		}
	}
	return err
}

func (l *listener) Addr() net.Addr {
	a := &addr{port: l.port}
	if l.ipAddress != nil {
		a.ipAddress = *l.ipAddress
	}
	return a
}

// Dial returns a net.Conn bound to the given address.
// No network calls/handshakes are performed.
func (l *listener) Dial(ctx context.Context, address string) (net.Conn, error) {
	port, ipAddress, err := parseHostPort(address, true /*needIP*/)
	if err != nil {
		return nil, fmt.Errorf("error parsing address: %w", err)
	}
	c := l.findConnOrCreate(addr{port, *ipAddress})
	if err := c.protocolHandshake(ctx); err != nil {
		return nil, fmt.Errorf("error performing protocol handshake: %w", err)
	}
	return c, nil
}

func (l *listener) findConnOrCreate(remoteAddr addr) conn {
	l.connsMu.Lock()
	defer l.connsMu.Unlock()

	c, ok := l.conns[remoteAddr]
	if !ok {
		c = l.factory.newConn(l, remoteAddr)
		l.conns[remoteAddr] = c
	}
	return c
}

func (l *listener) findConnOrCreatePending(remoteAddr addr) conn {
	l.connsMu.Lock()
	defer l.connsMu.Unlock()

	// check already existing conn
	if c, ok := l.conns[remoteAddr]; ok {
		return c
	}

	// no already existing conn, need a new one. check if listener was Close()d first
	if l.pendingConns == nil {
		return nil // drop rule: port stopped listening
	}

	// port is listening. find or create a new pending conn
	c, ok := l.pendingConns[remoteAddr]
	if !ok {
		c = l.factory.newConn(l, remoteAddr)
		l.pendingConns[remoteAddr] = c
	}
	l.connsCond.Broadcast() // unblock Accept()

	return c
}
