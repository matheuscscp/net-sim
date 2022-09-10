package transport

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/matheuscscp/net-sim/layers/common"
	"github.com/matheuscscp/net-sim/layers/network"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/hashicorp/go-multierror"
)

type (
	// UDPConn implements net.Conn for UDP.
	UDPConn struct {
		l          *listener
		remoteAddr addr

		inMu         sync.Mutex
		inFront      *udpQueueElem
		inBack       *udpQueueElem
		inCond       *sync.Cond
		readDeadline time.Time
		closed       bool
	}

	udp struct{}

	udpQueueElem struct {
		payload []byte
		next    *udpQueueElem
	}
)

func newUDP(ctx context.Context, networkLayer network.Layer) *listenerSet {
	return newListenerSet(ctx, networkLayer, &udp{})
}

func (*udp) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return DeserializeUDPSegment(datagram)
}

func DeserializeUDPSegment(datagram *gplayers.IPv4) (*gplayers.UDP, error) {
	// deserialize
	pkt := gopacket.NewPacket(datagram.Payload, gplayers.LayerTypeUDP, gopacket.Lazy)
	segment := pkt.TransportLayer().(*gplayers.UDP)
	if segment == nil || len(segment.Payload) == 0 {
		return nil, fmt.Errorf("error deserializing udp layer: %w", pkt.ErrorLayer().Error())
	}

	// validate checksum
	checksum := segment.Checksum
	if err := segment.SetNetworkLayerForChecksum(datagram); err != nil {
		return nil, fmt.Errorf("error setting network layer for checksum: %w", err)
	}
	err := gopacket.SerializeLayers(
		gopacket.NewSerializeBuffer(),
		gopacket.SerializeOptions{ComputeChecksums: true},
		segment,
		gopacket.Payload(segment.Payload),
	)
	if err != nil {
		return nil, fmt.Errorf("error calculating checksum (reserializing): %w", err)
	}
	if segment.Checksum != checksum {
		return nil, fmt.Errorf("checksums differ. want %d, got %d", segment.Checksum, checksum)
	}

	return segment, nil
}

func (*udp) newConn(l *listener, remoteAddr addr) conn {
	c := &UDPConn{
		l:          l,
		remoteAddr: remoteAddr,
	}
	c.inCond = sync.NewCond(&c.inMu)
	return c
}

func (u *UDPConn) protocolHandshake(ctx context.Context) error {
	return nil // no-op
}

func (u *UDPConn) recv(segment gopacket.TransportLayer) {
	u.pushRead(segment.LayerPayload())
}

func (u *UDPConn) pushRead(payload []byte) {
	e := &udpQueueElem{payload: payload}

	u.inMu.Lock()
	defer u.inMu.Unlock()

	if u.closed {
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

func (u *UDPConn) checkReadCondition() error {
	// check closed
	if u.closed {
		return ErrConnClosed
	}

	// check deadline
	if !u.readDeadline.IsZero() && u.readDeadline.Before(time.Now()) {
		return ErrTimeout
	}

	return nil
}

// Read blocks waiting for one UDP segment. b must have enough space for
// the whole UDP segment payload, otherwise the exceeding part will be
// lost.
func (u *UDPConn) Read(b []byte) (n int, err error) {
	u.inMu.Lock()

	// wait until one of these happens: data becomes available,
	// the conn is Close()d, or the deadline is exceeded
	if err := u.checkReadCondition(); err != nil {
		u.inMu.Unlock()
		return 0, err
	}
	for u.inFront == nil {
		u.inCond.Wait()
		if err := u.checkReadCondition(); err != nil {
			u.inMu.Unlock()
			return 0, err
		}
	}

	// data is now available, pop the queue
	e := u.inFront
	u.inFront = e.next
	if u.inFront == nil {
		u.inBack = nil
	}
	u.inMu.Unlock() // unlock before copy()ing for performance
	return copy(b, e.payload), nil
}

func (u *UDPConn) Write(b []byte) (n int, err error) {
	if u.closed {
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
	intf, err := u.l.networkLayer.FindInterfaceForHeader(datagramHeader)
	if err != nil {
		return 0, fmt.Errorf("error finding interface for datagram header: %w", err)
	}
	if err := intf.SendTransportSegment(context.Background(), datagramHeader, segment); err != nil {
		return 0, fmt.Errorf("error sending IP datagram: %w", err)
	}

	return len(b), nil
}

func (u *UDPConn) Close() error {
	// first, remove conn from listener so arriving segments are
	// not directed to this conn anymore as soon as possible
	u.l.connsMu.Lock()
	delete(u.l.conns, u.remoteAddr)
	u.l.connsMu.Unlock()

	// close conn
	u.inMu.Lock()
	u.closed = true
	u.inFront = nil
	u.inCond.Broadcast()
	u.inMu.Unlock()

	return nil
}

func (u *UDPConn) LocalAddr() net.Addr {
	return u.l.Addr()
}

func (u *UDPConn) RemoteAddr() net.Addr {
	a := u.remoteAddr
	return &a
}

// SetDeadline is the same as calling SetReadDeadline() and
// SetWriteDeadline().
func (u *UDPConn) SetDeadline(d time.Time) error {
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
// is reached, so calling it with a time point that is
// too distant in the future is not a good idea.
func (u *UDPConn) SetReadDeadline(d time.Time) error {
	u.inMu.Lock()
	u.readDeadline = d
	u.inMu.Unlock()

	// start thread to wait until either the deadline or the
	// transport layer context is done and then notify all
	// blocked readers
	go func() {
		defer u.inCond.Broadcast() // notify blocked readers
		timer := time.NewTimer(time.Until(d))
		select {
		case <-u.l.ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}()

	return nil
}

func (u *UDPConn) SetWriteDeadline(d time.Time) error {
	return nil // no-op
}
