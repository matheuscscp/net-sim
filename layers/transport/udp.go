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
	"github.com/sirupsen/logrus"
)

type (
	// UDPListener implements net.Listener for UDP.
	UDPListener struct {
		ctx          context.Context
		networkLayer network.Layer
		port         gplayers.UDPPort
		ipAddress    *gopacket.Endpoint

		connsMu             sync.Mutex
		conns, pendingConns map[udpAddr]*UDPConn
		connsCond           *sync.Cond
	}

	// UDPConn implements net.Conn for UDP.
	UDPConn struct {
		l          *UDPListener
		remoteAddr udpAddr

		inMu         sync.Mutex
		inFront      *udpQueueElem
		inBack       *udpQueueElem
		inCond       *sync.Cond
		readDeadline time.Time
		closed       bool
	}

	udp struct {
		ctx          context.Context
		networkLayer network.Layer

		listenersMu sync.RWMutex
		listeners   map[gplayers.UDPPort]*UDPListener
	}

	udpAddr struct {
		port      gplayers.UDPPort
		ipAddress gopacket.Endpoint
	}

	udpQueueElem struct {
		payload []byte
		next    *udpQueueElem
	}
)

func newUDP(ctx context.Context, networkLayer network.Layer) *udp {
	return &udp{
		ctx:          ctx,
		networkLayer: networkLayer,

		listeners: make(map[gplayers.UDPPort]*UDPListener),
	}
}

func (u *udp) listen(address string) (*UDPListener, error) {
	// parse address
	intPort, ipAddress, err := parseHostPort(address, false /*needIP*/)
	if err != nil {
		return nil, fmt.Errorf("error parsing address: %w", err)
	}
	port := gplayers.UDPPort(intPort)

	u.listenersMu.Lock()
	defer u.listenersMu.Unlock()

	// if port is zero, choose a free port
	if port == 0 {
		for p := gplayers.UDPPort(65535); 1 <= p; p-- {
			if _, ok := u.listeners[p]; !ok {
				port = p
				break
			}
		}
		if port == 0 {
			return nil, ErrAllPortsAlreadyInUse
		}
	}

	if _, ok := u.listeners[port]; ok {
		return nil, ErrPortAlreadyInUse
	}

	l := &UDPListener{
		networkLayer: u.networkLayer,
		ctx:          u.ctx,
		port:         port,
		ipAddress:    ipAddress,
		conns:        make(map[udpAddr]*UDPConn),
		pendingConns: make(map[udpAddr]*UDPConn),
	}
	l.connsCond = sync.NewCond(&l.connsMu)
	u.listeners[port] = l

	return l, nil
}

func (u *udp) dial(ctx context.Context, address string) (*UDPConn, error) {
	// parse remote address
	intPort, ipAddress, err := parseHostPort(address, true /*needIP*/)
	if err != nil {
		return nil, fmt.Errorf("error parsing address: %w", err)
	}
	port := gplayers.UDPPort(intPort)

	// allocate random port
	l, err := u.listen(":0")
	if err != nil {
		return nil, fmt.Errorf("error trying to listen on a free port: %w", err)
	}

	// stop accepting connections
	if err := l.Close(); err != nil {
		return nil, fmt.Errorf("error closing local listener for other connections: %w", err)
	}

	return l.findOrCreateConn(udpAddr{port, *ipAddress}), nil
}

func (u *udp) decapAndDemux(datagram *gplayers.IPv4) {
	// decap
	segment, err := DeserializeUDPSegment(datagram)
	if err != nil {
		logrus.
			WithError(err).
			WithField("datagram", datagram).
			Error("error decapsulating transport layer")
		return
	}

	// find listener
	dstPort, dstIPAddress := segment.DstPort, gplayers.NewIPEndpoint(datagram.DstIP)
	u.listenersMu.RLock()
	l, ok := u.listeners[dstPort]
	u.listenersMu.RUnlock()
	if !ok {
		return // drop rule: port is not listening
	}
	if l.ipAddress != nil && *l.ipAddress != dstIPAddress {
		return // drop rule: port is listening but dst IP address does not match the IP bound by the port
	}

	// demux
	srcPort, srcIPAddress := segment.SrcPort, gplayers.NewIPEndpoint(datagram.SrcIP)
	remoteAddr := udpAddr{srcPort, srcIPAddress}
	if c := l.demux(remoteAddr); c != nil {
		c.pushRead(segment.Payload)
	}
}

func (u *UDPListener) Accept() (net.Conn, error) {
	u.connsMu.Lock()
	defer u.connsMu.Unlock()

	// wait until one of these happens: a new conn arrives,
	// or the listener is Close()d
	if u.pendingConns == nil {
		return nil, ErrListenerClosed
	}
	for len(u.pendingConns) == 0 {
		u.connsCond.Wait()
		if u.pendingConns == nil {
			return nil, ErrListenerClosed
		}
	}

	// a conn is now availble, pick the first
	for addr, conn := range u.pendingConns {
		delete(u.pendingConns, addr)
		u.conns[addr] = conn
		return conn, nil
	}
	return nil, nil
}

func (u *UDPListener) Close() error {
	// first, delete the pendingConns map so new conns are dropped
	// as soon as possible
	u.connsMu.Lock()
	pendingConns := u.pendingConns
	u.pendingConns = nil
	u.connsCond.Broadcast()
	u.connsMu.Unlock()

	// close pending conns
	var err error
	for addr, pendingConn := range pendingConns {
		if cErr := pendingConn.Close(); cErr != nil {
			err = multierror.Append(err, fmt.Errorf("error closing pending connection from %s: %w", addr.String(), cErr))
		}
	}
	return err
}

func (u *UDPListener) Addr() net.Addr {
	a := &udpAddr{port: u.port}
	if u.ipAddress != nil {
		a.ipAddress = *u.ipAddress
	}
	return a
}

// Dial returns a UDP net.Conn bound to the given address.
// No network calls/handshakes are performed.
func (u *UDPListener) Dial(address string) (net.Conn, error) {
	intPort, ipAddress, err := parseHostPort(address, true /*needIP*/)
	if err != nil {
		return nil, fmt.Errorf("error parsing address: %w", err)
	}
	port := gplayers.UDPPort(intPort)
	return u.findOrCreateConn(udpAddr{port, *ipAddress}), nil
}

func (u *UDPListener) findOrCreateConn(remoteAddr udpAddr) *UDPConn {
	u.connsMu.Lock()
	defer u.connsMu.Unlock()

	c, ok := u.conns[remoteAddr]
	if !ok {
		c = newUDPConn(u, remoteAddr)
		u.conns[remoteAddr] = c
	}
	return c
}

func (u *UDPListener) demux(remoteAddr udpAddr) *UDPConn {
	u.connsMu.Lock()
	defer u.connsMu.Unlock()

	// check already existing conn
	if c, ok := u.conns[remoteAddr]; ok {
		return c
	}

	// no already existing conn, need a new one. check if listener was Close()d first
	if u.pendingConns == nil {
		return nil // drop rule: port stopped listening
	}

	// port is listening. find or create a new pending conn
	c, ok := u.pendingConns[remoteAddr]
	if !ok {
		c = newUDPConn(u, remoteAddr)
		u.pendingConns[remoteAddr] = c
	}
	u.connsCond.Broadcast() // unblock Accept()

	return c
}

func newUDPConn(l *UDPListener, remoteAddr udpAddr) *UDPConn {
	c := &UDPConn{
		l:          l,
		remoteAddr: remoteAddr,
	}
	c.inCond = sync.NewCond(&c.inMu)
	return c
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
		SrcPort: u.l.port,
		DstPort: u.remoteAddr.port,
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

func (u *udpAddr) Network() string {
	return UDP
}

func (u *udpAddr) String() string {
	return fmt.Sprintf("%s:%d", u.ipAddress, u.port)
}

func DeserializeUDPSegment(datagram *gplayers.IPv4) (*gplayers.UDP, error) {
	// deserialize
	pkt := gopacket.NewPacket(datagram.Payload, gplayers.LayerTypeUDP, gopacket.Lazy)
	segment := pkt.TransportLayer().(*gplayers.UDP)
	if segment == nil || len(segment.Payload) == 0 {
		return nil, fmt.Errorf("error deserializing transport layer: %w", pkt.ErrorLayer().Error())
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
