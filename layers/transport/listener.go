package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	pkgio "github.com/matheuscscp/net-sim/pkg/io"

	petname "github.com/dustinkirkland/golang-petname"
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
	logger := l.connLogger(c)
	logger.Debug("Accept(): conn created")

	// a handshake should not block the goroutine calling Accept(),
	// hence we call it on a new goroutine instead. the same is not
	// true when Dial()ing
	ctx, cancel := context.WithTimeout(l.acceptCtx, 5*time.Second)
	c.setHandshakeContext(ctx)
	go func() {
		defer cancel()
		if err := c.doHandshake(); err != nil {
			c.Close()
			logger.
				WithError(err).
				Error("error doing server handshake. connection was closed")
		} else {
			logger.Debug("success doing server handshake")
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
	// delete the conns map
	var conns map[addr]conn
	l.connsMu.Lock()
	conns, l.conns = l.conns, nil
	if conns == nil {
		l.connsMu.Unlock()
		return nil
	}
	l.connsMu.Unlock()
	l.s.deleteListener(l)

	// close conns
	closers := make([]io.Closer, 0, len(conns))
	for _, c := range conns {
		closers = append(closers, c)
	}
	err := pkgio.Close(closers...)

	if sErr := l.stopListening(); sErr != nil {
		err = multierror.Append(err, sErr)
	}

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
	closers := make([]io.Closer, 0, len(pendingConns))
	for _, c := range pendingConns {
		closers = append(closers, c)
	}
	return pkgio.Close(closers...)
}

func (l *listener) Addr() net.Addr {
	a := addr{port: l.port}
	if l.ipAddress != nil {
		a.ipAddress = *l.ipAddress
	}
	return l.s.protocolFactory.newAddr(a)
}

// Dial returns a net.Conn bound to the given address.
// No network calls/handshakes are performed.
func (l *listener) Dial(ctx context.Context, address string) (net.Conn, error) {
	// find conn or create
	port, ipAddress, err := parseHostPort(address, true /*needIP*/)
	if err != nil {
		return nil, fmt.Errorf("error parsing address: %w", err)
	}
	c, err := l.createConn(addr{port, *ipAddress})
	if err != nil {
		return nil, err
	}
	logger := l.connLogger(c)
	logger.Debug("Dial(): conn created")

	// handshake
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.setHandshakeContext(ctx)
	if err := c.doHandshake(); err != nil {
		c.Close()
		const msg = "error doing client handshake. connection was closed"
		logger.
			WithError(err).
			Error(msg)
		return nil, fmt.Errorf("%s: %w", msg, err)
	}
	logger.Debug("success doing client handshake")
	return c, nil
}

func (l *listener) createConn(remoteAddr addr) (conn, error) {
	l.connsMu.Lock()
	defer l.connsMu.Unlock()

	if l.conns == nil {
		return nil, ErrListenerClosed
	}

	if _, ok := l.conns[remoteAddr]; ok {
		return nil, fmt.Errorf("a connection to %s already exists", remoteAddr.String())
	}

	c := l.s.protocolFactory.newConn(l, remoteAddr, l.s.protocolFactory.newClientHandshake())
	l.conns[remoteAddr] = c
	return c, nil
}

func (l *listener) findConnOrCreatePending(remoteAddr addr, segment gopacket.TransportLayer) conn {
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
	if !l.s.protocolFactory.shouldCreatePendingConn(segment) {
		return nil
	}
	l.pendingConnsMu.Lock()
	defer l.pendingConnsMu.Unlock()

	// now we need to check for an already existing conn again,
	// because between requesting and acquiring the pendingConns
	// lock it's possible that Accept() added our conn to l.conns
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
		c = l.s.protocolFactory.newConn(l, remoteAddr, l.s.protocolFactory.newServerHandshake())
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

func (l *listener) connLogger(c conn) logrus.FieldLogger {
	a := l.Addr()
	return logrus.
		WithField("debug_conn_name", petname.Generate(2, "_")).
		WithField("protocol", a.Network()).
		WithField("local_addr", a.String()).
		WithField("remote_addr", c.RemoteAddr().String())
}
