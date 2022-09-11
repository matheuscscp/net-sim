package transport

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/matheuscscp/net-sim/layers/common"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/hashicorp/go-multierror"
)

type (
	udp struct{}

	udpConn struct {
		ctx        context.Context
		cancelCtx  context.CancelFunc
		l          *listener
		remoteAddr addr

		inMu         sync.Mutex
		inFront      *udpQueueElem
		inBack       *udpQueueElem
		inCond       *sync.Cond
		readDeadline time.Time
		closed       bool
	}

	udpQueueElem struct {
		payload []byte
		next    *udpQueueElem
	}
)

func (*udp) newConn(l *listener, remoteAddr addr) conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &udpConn{
		ctx:        ctx,
		cancelCtx:  cancel,
		l:          l,
		remoteAddr: remoteAddr,
	}
	c.inCond = sync.NewCond(&c.inMu)
	return c
}

func (*udp) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return DeserializeUDPSegment(datagram)
}

func (c *udpConn) handshakeDial(ctx context.Context) error {
	return nil // no-op
}

func (c *udpConn) handshakeAccept(ctx context.Context) error {
	return nil // no-op
}

func (c *udpConn) recv(segment gopacket.TransportLayer) {
	c.pushRead(segment.LayerPayload())
}

func (c *udpConn) pushRead(payload []byte) {
	e := &udpQueueElem{payload: payload}

	c.inMu.Lock()
	defer c.inMu.Unlock()

	if c.closed {
		return
	}

	if c.inBack == nil {
		c.inFront = e
		c.inBack = e
	} else {
		c.inBack.next = e
		c.inBack = e
	}
	c.inCond.Signal()
}

func (c *udpConn) checkReadCondition() error {
	// check closed
	if c.closed {
		return ErrConnClosed
	}

	// check deadline
	if !c.readDeadline.IsZero() && c.readDeadline.Before(time.Now()) {
		return ErrTimeout
	}

	return nil
}

// Read blocks waiting for one UDP segment. b must have enough space for
// the whole UDP segment payload, otherwise the exceeding part will be
// lost.
func (c *udpConn) Read(b []byte) (n int, err error) {
	c.inMu.Lock()

	// wait until one of these happens: data becomes available,
	// the conn is Close()d, or the deadline is exceeded
	if err := c.checkReadCondition(); err != nil {
		c.inMu.Unlock()
		return 0, err
	}
	for c.inFront == nil {
		c.inCond.Wait()
		if err := c.checkReadCondition(); err != nil {
			c.inMu.Unlock()
			return 0, err
		}
	}

	// data is now available, pop the queue
	e := c.inFront
	c.inFront = e.next
	if c.inFront == nil {
		c.inBack = nil
	}
	c.inMu.Unlock() // unlock before copy()ing for performance
	return copy(b, e.payload), nil
}

func (c *udpConn) Write(b []byte) (n int, err error) {
	if c.closed {
		return 0, ErrConnClosed
	}

	// validate payload size
	if len(b) == 0 {
		return 0, common.ErrCannotSendEmpty
	}
	if len(b) > UDPMTU {
		return 0, fmt.Errorf("payload is larger than transport layer UDP MTU (%d)", UDPMTU)
	}

	// send
	datagramHeader := &gplayers.IPv4{
		DstIP:    c.remoteAddr.ipAddress.Raw(),
		Protocol: gplayers.IPProtocolUDP,
	}
	segment := &gplayers.UDP{
		BaseLayer: gplayers.BaseLayer{
			Payload: b,
		},
		SrcPort: gplayers.UDPPort(c.l.port),
		DstPort: gplayers.UDPPort(c.remoteAddr.port),
		Length:  uint16(len(b) + UDPHeaderLength),
	}
	if c.l.ipAddress != nil {
		datagramHeader.SrcIP = c.l.ipAddress.Raw()
	}
	intf, err := c.l.networkLayer.FindInterfaceForHeader(datagramHeader)
	if err != nil {
		return 0, fmt.Errorf("error finding interface for datagram header: %w", err)
	}
	if err := intf.SendTransportSegment(c.ctx, datagramHeader, segment); err != nil {
		return 0, fmt.Errorf("error sending IP datagram: %w", err)
	}

	return len(b), nil
}

func (c *udpConn) Close() error {
	// cancel ctx
	var cancel context.CancelFunc
	cancel, c.cancelCtx = c.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()

	// remove conn from listener so arriving segments are
	// not directed to this conn anymore
	c.l.connsMu.Lock()
	delete(c.l.conns, c.remoteAddr)
	c.l.connsMu.Unlock()

	// close conn
	c.inMu.Lock()
	c.closed = true
	c.inFront = nil
	c.inCond.Broadcast()
	c.inMu.Unlock()

	return nil
}

func (c *udpConn) LocalAddr() net.Addr {
	return c.l.Addr()
}

func (c *udpConn) RemoteAddr() net.Addr {
	a := c.remoteAddr
	return &a
}

// SetDeadline is the same as calling SetReadDeadline() and
// SetWriteDeadline().
func (c *udpConn) SetDeadline(d time.Time) error {
	var err error
	if dErr := c.SetReadDeadline(d); dErr != nil {
		err = multierror.Append(err, fmt.Errorf("error setting read deadline: %w", err))
	}
	if dErr := c.SetWriteDeadline(d); dErr != nil {
		err = multierror.Append(err, fmt.Errorf("error setting write deadline: %w", err))
	}
	return err
}

// SetReadDeadline sets the read deadline. This method
// starts a thread that only returns when the deadline
// is reached (or when the connection is Close()d), so
// calling it with a time point that is too distant in
// the future is not a good idea.
func (c *udpConn) SetReadDeadline(d time.Time) error {
	c.inMu.Lock()
	c.readDeadline = d
	c.inMu.Unlock()

	// start thread to wait until either the deadline or until
	// the connection context is done and then notify all blocked
	// readers
	go func() {
		// wait
		ctx, cancel := context.WithDeadline(c.ctx, d)
		defer cancel()

		// notify
		<-ctx.Done()
		c.inCond.Broadcast()
	}()

	return nil
}

func (c *udpConn) SetWriteDeadline(d time.Time) error {
	return nil // no-op
}
