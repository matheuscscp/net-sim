package transport

import (
	"context"
	"errors"
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

		outMu         sync.Mutex
		outCond       *sync.Cond
		writeDeadline time.Time
	}

	udpQueueElem struct {
		payload []byte
		next    *udpQueueElem
	}
)

func (udp) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return DeserializeUDPSegment(datagram)
}

func (udp) newClientHandshake() handshake {
	return nil // no-op
}

func (udp) newServerHandshake() handshake {
	return nil // no-op
}

func (udp) newConn(l *listener, remoteAddr addr, _ handshake) conn {
	ctx, cancel := context.WithCancel(context.Background())
	u := &udpConn{
		ctx:        ctx,
		cancelCtx:  cancel,
		l:          l,
		remoteAddr: remoteAddr,
	}
	u.inCond = sync.NewCond(&u.inMu)
	u.outCond = sync.NewCond(&u.outMu)
	return u
}

func (u *udpConn) setHandshakeContext(ctx context.Context) {
	// no-op
}

func (u *udpConn) handshake() error {
	return nil // no-op
}

func (u *udpConn) recv(segment gopacket.TransportLayer) {
	e := &udpQueueElem{payload: segment.LayerPayload()}

	u.inMu.Lock()
	defer u.inMu.Unlock()

	if u.ctx.Err() != nil {
		return
	}

	if u.inBack == nil {
		u.inFront = e
		u.inBack = e
	} else {
		u.inBack.next = e
		u.inBack = e
	}
	u.inCond.Signal()
}

// Read blocks waiting for one UDP segment. b must have enough space for
// the whole UDP segment payload, otherwise the exceeding part will be
// lost.
func (u *udpConn) Read(b []byte) (n int, err error) {
	e, err := u.waitForQueueElem()
	if err != nil {
		return 0, err
	}
	return copy(b, e.payload), nil
}

func (u *udpConn) waitForQueueElem() (*udpQueueElem, error) {
	u.inMu.Lock()
	defer u.inMu.Unlock()

	// wait until one of these happens: data becomes available,
	// the conn is Close()d, or the deadline is exceeded
	if err := u.checkReadError(); err != nil {
		return nil, err
	}
	for u.inFront == nil {
		u.inCond.Wait()
		if err := u.checkReadError(); err != nil {
			return nil, err
		}
	}

	// data is now available, pop the queue
	var elem *udpQueueElem
	elem, u.inFront = u.inFront, u.inFront.next
	elem.next = nil
	if u.inFront == nil {
		u.inBack = nil
	}
	return elem, nil
}

func (u *udpConn) checkReadError() error {
	// check closed
	if u.ctx.Err() != nil {
		return ErrConnClosed
	}

	// check deadline
	if !u.readDeadline.IsZero() && u.readDeadline.Before(time.Now()) {
		return ErrDeadlineExceeded
	}

	return nil
}

func (u *udpConn) Write(b []byte) (n int, err error) {
	if u.ctx.Err() != nil {
		return 0, ErrConnClosed
	}

	// validate payload size
	if len(b) == 0 {
		return 0, common.ErrCannotSendEmpty
	}
	if len(b) > UDPMTU {
		return 0, fmt.Errorf("payload is larger than transport layer UDP MTU (%d)", UDPMTU)
	}

	// craft headers and find interface
	datagramHeader := &gplayers.IPv4{
		DstIP:    u.remoteAddr.ipAddress.Raw(),
		Protocol: gplayers.IPProtocolUDP,
	}
	segment := &gplayers.UDP{
		BaseLayer: gplayers.BaseLayer{
			Payload: b,
		},
		SrcPort: gplayers.UDPPort(u.l.port),
		DstPort: gplayers.UDPPort(u.remoteAddr.port),
		Length:  uint16(len(b) + UDPHeaderLength),
	}
	if u.l.ipAddress != nil {
		datagramHeader.SrcIP = u.l.ipAddress.Raw()
	}
	intf, err := u.l.s.networkLayer.FindInterfaceForHeader(datagramHeader)
	if err != nil {
		return 0, fmt.Errorf("error finding interface for datagram header: %w", err)
	}

	// start deadline thread
	ctx, cancel := context.WithCancel(u.ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		u.outMu.Lock()
		u.outCond.Broadcast()
		u.outMu.Unlock()
		wg.Wait()
	}()
	wg.Add(1)
	var deadlineExceeded bool
	go func() {
		defer func() {
			cancel()
			wg.Done()
		}()

		u.outMu.Lock()
		for ctx.Err() == nil && !u.writeDeadlineExceeded() {
			u.outCond.Wait()
		}
		u.outMu.Unlock()

		if ctx.Err() == nil {
			deadlineExceeded = true
		}
	}()

	// send
	if u.writeDeadlineExceeded() {
		return 0, ErrDeadlineExceeded
	}
	if err := intf.SendTransportSegment(ctx, datagramHeader, segment); err != nil {
		if deadlineExceeded {
			return 0, ErrDeadlineExceeded
		}
		if errors.Is(err, context.Canceled) {
			return 0, ErrConnClosed
		}
		return 0, fmt.Errorf("error sending udp segment: %w", err)
	}

	return len(b), nil
}

func (u *udpConn) writeDeadlineExceeded() bool {
	return !u.writeDeadline.IsZero() && u.writeDeadline.Before(time.Now())
}

func (u *udpConn) Close() error {
	// remove conn from listener so arriving segments are
	// not directed to this conn anymore
	u.l.deleteConn(u.remoteAddr)

	// cancel ctx (unblock writers)
	var cancel context.CancelFunc
	cancel, u.cancelCtx = u.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()

	// unblock readers
	u.inMu.Lock()
	u.inCond.Broadcast()
	u.inMu.Unlock()

	return nil
}

func (u *udpConn) LocalAddr() net.Addr {
	return u.l.Addr()
}

func (u *udpConn) RemoteAddr() net.Addr {
	a := u.remoteAddr
	return &a
}

// SetDeadline is the same as calling SetReadDeadline() and
// SetWriteDeadline().
func (u *udpConn) SetDeadline(d time.Time) error {
	var err error
	if dErr := u.SetReadDeadline(d); dErr != nil {
		err = multierror.Append(err, fmt.Errorf("error setting read deadline: %w", err))
	}
	if dErr := u.SetWriteDeadline(d); dErr != nil {
		err = multierror.Append(err, fmt.Errorf("error setting write deadline: %w", err))
	}
	return err
}

// SetReadDeadline sets the read deadline. This method
// starts a thread that only returns when the deadline
// is reached (or when the connection is Close()d), so
// calling it with a time point that is too distant in
// the future is not a good idea.
func (u *udpConn) SetReadDeadline(d time.Time) error {
	u.inMu.Lock()
	u.readDeadline = d
	u.inMu.Unlock()

	// start thread to wait until either the deadline or until
	// the connection context is done and then notify all blocked
	// readers
	go func() {
		// wait
		ctx, cancel := context.WithDeadline(u.ctx, d)
		defer cancel()

		// notify
		<-ctx.Done()
		u.inCond.Broadcast()
	}()

	return nil
}

// SetWriteDeadline sets the write deadline. This method
// starts a thread that only returns when the deadline
// is reached (or when the connection is Close()d), so
// calling it with a time point that is too distant in
// the future is not a good idea.
func (u *udpConn) SetWriteDeadline(d time.Time) error {
	u.outMu.Lock()
	u.writeDeadline = d
	u.outMu.Unlock()

	// start thread to wait until either the deadline or until
	// the connection context is done and then notify all blocked
	// writers
	go func() {
		// wait
		ctx, cancel := context.WithDeadline(u.ctx, d)
		defer cancel()

		// notify
		<-ctx.Done()
		u.outCond.Broadcast()
	}()

	return nil
}
