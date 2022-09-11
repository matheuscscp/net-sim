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
		cancelCtx    context.CancelFunc
		networkLayer network.Layer
		factory      protocolFactory
		port         uint16
		ipAddress    *gopacket.Endpoint

		connsMu, pendingConnsMu sync.RWMutex
		conns, pendingConns     map[addr]conn
		pendingConnsCond        *sync.Cond
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
	ctx, cancel := context.WithCancel(ctx)
	l := &listener{
		ctx:          ctx,
		cancelCtx:    cancel,
		networkLayer: networkLayer,
		factory:      factory,
		port:         port,
		ipAddress:    ipAddress,
		conns:        make(map[addr]conn),
		pendingConns: make(map[addr]conn),
	}
	l.pendingConnsCond = sync.NewCond(&l.pendingConnsMu)
	return l
}

func (l *listener) matchesDstIPAddress(dstIPAddress gopacket.Endpoint) bool {
	return l.ipAddress == nil || *l.ipAddress == dstIPAddress
}

func (l *listener) Accept() (net.Conn, error) {
	c, err := l.waitForPendingConnAndMoveToConns()
	if err != nil {
		return nil, fmt.Errorf("error waiting for pending connection: %w", err)
	}

	if err := c.handshakeAccept(l.ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("error accepting protocol handshake: %w", err)
	}

	return c, nil
}

func (l *listener) waitForPendingConnAndMoveToConns() (conn, error) {
	l.pendingConnsMu.Lock()
	defer l.pendingConnsMu.Unlock()

	// wait until one of these happens: a new conn arrives,
	// or the listener is Close()d
	if l.pendingConns == nil {
		return nil, ErrListenerClosed
	}
	for len(l.pendingConns) == 0 {
		l.pendingConnsCond.Wait()
		if l.pendingConns == nil {
			return nil, ErrListenerClosed
		}
	}

	// a pending conn is now availble, pick the first and remove from map
	var a addr
	var c conn
	for a, c = range l.pendingConns {
		break
	}
	delete(l.pendingConns, a)

	// add to accepted conns
	l.connsMu.Lock()
	l.conns[a] = c
	l.connsMu.Unlock()

	return c, nil
}

func (l *listener) Close() error {
	// cancel ctx
	var cancel context.CancelFunc
	cancel, l.cancelCtx = l.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()

	// delete the pendingConns map so new conns are dropped
	l.pendingConnsMu.Lock()
	pendingConns := l.pendingConns
	l.pendingConns = nil
	l.pendingConnsCond.Broadcast()
	l.pendingConnsMu.Unlock()

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
	// find conn or create
	port, ipAddress, err := parseHostPort(address, true /*needIP*/)
	if err != nil {
		return nil, fmt.Errorf("error parsing address: %w", err)
	}
	c := l.findConnOrCreate(addr{port, *ipAddress})

	// handshake
	if err := c.handshakeDial(ctx); err != nil {
		return nil, fmt.Errorf("error dialing protocol handshake: %w", err)
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
	// check for an already existing conn (this is the most common case
	// and therefore should be optimized by using an RWMutex)
	l.connsMu.RLock()
	c, ok := l.conns[remoteAddr]
	l.connsMu.RUnlock()
	if ok {
		return c
	}

	// conn does not exist. acquire pendingConns lock
	l.pendingConnsMu.Lock()
	defer l.pendingConnsMu.Unlock()

	// now we need to check for an already existing conn again,
	// because between requesting and acquiring the pendingConns
	// lock it's possible that accept() added our conn to l.conns
	l.connsMu.RLock()
	c, ok = l.conns[remoteAddr]
	l.connsMu.RUnlock()
	if ok {
		return c
	}

	// no already existing conn, and we hold the pendingConns lock.
	// time to create a pending conn, but, first, check if listener
	// was Close()d
	if l.pendingConns == nil {
		return nil // drop rule: port was Close()d (stopped listening)
	}

	// port is listening. find or create a new pending conn
	c, ok = l.pendingConns[remoteAddr]
	if !ok {
		c = l.factory.newConn(l, remoteAddr)
		l.pendingConns[remoteAddr] = c
		l.pendingConnsCond.Broadcast() // unblock Accept()
	}

	return c
}
