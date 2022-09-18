package transport

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/google/gopacket"
	"github.com/hashicorp/go-multierror"
	"github.com/sirupsen/logrus"
)

type (
	Dialer interface {
		Dial(ctx context.Context, address string) (net.Conn, error)
	}

	listener struct {
		acceptCtx       context.Context
		cancelAcceptCtx context.CancelFunc
		s               *listenerSet
		port            uint16
		ipAddress       *gopacket.Endpoint

		connsMu, pendingConnsMu sync.RWMutex
		conns, pendingConns     map[addr]conn
		pendingConnsCond        *sync.Cond
	}
)

var (
	_ Dialer = (*listener)(nil)
)

func newListener(
	s *listenerSet,
	port uint16,
	ipAddress *gopacket.Endpoint,
) *listener {
	acceptCtx, cancelAcceptCtx := context.WithCancel(context.Background())
	l := &listener{
		acceptCtx:       acceptCtx,
		cancelAcceptCtx: cancelAcceptCtx,
		s:               s,
		port:            port,
		ipAddress:       ipAddress,
		conns:           make(map[addr]conn),
		pendingConns:    make(map[addr]conn),
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

	ctx, cancel := context.WithTimeout(l.acceptCtx, 30*time.Second)
	c.setHandshakeContext(ctx)
	go func() {
		defer cancel()
		if err := c.handshake(); err != nil {
			c.Close()
			logrus.
				WithError(err).
				Error("error accepting protocol handshake")
		}
	}()

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
	defer l.connsMu.Unlock()
	if l.conns == nil {
		return nil, ErrListenerClosed
	}
	l.conns[a] = c

	return c, nil
}

func (l *listener) Close() error {
	if err := l.stopListening(); err != nil {
		return err
	}

	// delete the conns map so new conns are dropped
	var conns map[addr]conn
	l.connsMu.Lock()
	conns, l.conns = l.conns, nil
	if conns == nil {
		l.connsMu.Unlock()
		return nil
	}
	l.connsMu.Unlock()

	// close conns
	var err error
	for addr, conn := range conns {
		if cErr := conn.Close(); cErr != nil {
			err = multierror.Append(err, fmt.Errorf("error closing connection to %s: %w", addr.String(), cErr))
		}
	}

	l.s.deleteListener(l.port)

	return err
}

func (l *listener) stopListening() error {
	// delete the pendingConns map so new conns are dropped
	var pendingConns map[addr]conn
	l.pendingConnsMu.Lock()
	pendingConns, l.pendingConns = l.pendingConns, nil
	if pendingConns == nil {
		l.pendingConnsMu.Unlock()
		return nil
	}
	l.pendingConnsCond.Broadcast()
	l.pendingConnsMu.Unlock()

	l.cancelAcceptCtx()

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
	c, err := l.findConnOrCreate(addr{port, *ipAddress})
	if err != nil {
		return nil, err
	}

	// handshake
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.setHandshakeContext(ctx)
	if err := c.handshake(); err != nil {
		c.Close()
		return nil, fmt.Errorf("error dialing protocol handshake: %w", err)
	}
	return c, nil
}

func (l *listener) findConnOrCreate(remoteAddr addr) (conn, error) {
	l.connsMu.Lock()
	defer l.connsMu.Unlock()

	if l.conns == nil {
		return nil, ErrListenerClosed
	}

	c, ok := l.conns[remoteAddr]
	if !ok {
		c = l.s.factory.newConn(l, remoteAddr, l.s.factory.newClientHandshake())
		l.conns[remoteAddr] = c
	}
	return c, nil
}

func (l *listener) findConnOrCreatePending(remoteAddr addr) conn {
	// check for an already existing conn (this is the most common case
	// and therefore should be optimized by using an RWMutex)
	l.connsMu.RLock()
	if l.conns == nil {
		l.connsMu.RUnlock()
		return nil
	}
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
	if l.conns == nil {
		l.connsMu.RUnlock()
		return nil
	}
	c, ok = l.conns[remoteAddr]
	l.connsMu.RUnlock()
	if ok {
		return c
	}

	// no already existing conn, and we hold the pendingConns lock.
	// time to create a pending conn, but first check if stopListening()
	// was called
	if l.pendingConns == nil {
		return nil
	}

	// port is listening. find or create a new pending conn
	c, ok = l.pendingConns[remoteAddr]
	if !ok {
		c = l.s.factory.newConn(l, remoteAddr, l.s.factory.newServerHandshake())
		l.pendingConns[remoteAddr] = c
		l.pendingConnsCond.Broadcast() // unblock Accept()
	}

	return c
}

func (l *listener) deleteConn(remoteAddr addr) {
	l.connsMu.Lock()
	if l.conns != nil {
		delete(l.conns, remoteAddr)
	}
	l.connsMu.Unlock()
}
