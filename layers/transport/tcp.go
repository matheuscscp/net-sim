package transport

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"time"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/hashicorp/go-multierror"
)

type (
	tcp struct{}

	tcpConn struct {
		l          *listener
		remoteAddr addr
		h          handshake

		myAck   uint32
		peerAck uint32
	}

	tcpClientHandshake struct {
		synack chan uint32
	}

	tcpServerHandshake struct {
		syn chan struct{}
		ack chan uint32
	}
)

func (tcp) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return DeserializeTCPSegment(datagram)
}

func (tcp) newClientHandshake() handshake {
	return &tcpClientHandshake{make(chan uint32, 1)}
}

func (h *tcpClientHandshake) recv(segment gopacket.TransportLayer) {
	s := segment.(*gplayers.TCP)
	switch {
	case s.SYN && s.ACK:
		select {
		case h.synack <- s.Ack:
		default:
		}
	}
}

func (h *tcpClientHandshake) do(ctx context.Context, c conn) error {
	t := c.(*tcpConn)

	// send SYN segment
	datagramHeader := &gplayers.IPv4{
		DstIP:    t.remoteAddr.ipAddress.Raw(),
		Protocol: gplayers.IPProtocolTCP,
	}
	segment := &gplayers.TCP{
		SrcPort: gplayers.TCPPort(t.l.port),
		DstPort: gplayers.TCPPort(t.remoteAddr.port),
		SYN:     true,
	}
	if t.l.ipAddress != nil {
		datagramHeader.SrcIP = t.l.ipAddress.Raw()
	}
	intf, err := t.l.s.networkLayer.FindInterfaceForHeader(datagramHeader)
	if err != nil {
		return fmt.Errorf("error finding interface for datagram header: %w", err)
	}
	if err := intf.SendTransportSegment(ctx, datagramHeader, segment); err != nil {
		return fmt.Errorf("error sending tcp syn segment: %w", err)
	}

	// receive SYNACK segment
	var peerAck uint32
	select {
	case <-ctx.Done():
		return ctx.Err()
	case peerAck = <-h.synack:
	}

	// send ACK segment
	myAck := rand.Uint32()
	segment = &gplayers.TCP{
		SrcPort: gplayers.TCPPort(t.l.port),
		DstPort: gplayers.TCPPort(t.remoteAddr.port),
		ACK:     true,
		Ack:     myAck,
	}
	if err := intf.SendTransportSegment(ctx, datagramHeader, segment); err != nil {
		return fmt.Errorf("error sending tcp ack segment: %w", err)
	}

	t.myAck = myAck
	t.peerAck = peerAck

	return nil
}

func (tcp) newServerHandshake() handshake {
	return &tcpServerHandshake{make(chan struct{}, 1), make(chan uint32, 1)}
}

func (h *tcpServerHandshake) recv(segment gopacket.TransportLayer) {
	s := segment.(*gplayers.TCP)
	switch {
	case s.SYN && !s.ACK:
		select {
		case h.syn <- struct{}{}:
		default:
		}
	case s.ACK && !s.SYN:
		select {
		case h.ack <- s.Ack:
		default:
		}
	}
}

func (h *tcpServerHandshake) do(ctx context.Context, c conn) error {
	t := c.(*tcpConn)

	// receive SYN segment
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-h.syn:
	}

	// send SYNACK segment
	myAck := rand.Uint32()
	datagramHeader := &gplayers.IPv4{
		DstIP:    t.remoteAddr.ipAddress.Raw(),
		Protocol: gplayers.IPProtocolTCP,
	}
	segment := &gplayers.TCP{
		SrcPort: gplayers.TCPPort(t.l.port),
		DstPort: gplayers.TCPPort(t.remoteAddr.port),
		SYN:     true,
		ACK:     true,
		Ack:     myAck,
	}
	if t.l.ipAddress != nil {
		datagramHeader.SrcIP = t.l.ipAddress.Raw()
	}
	intf, err := t.l.s.networkLayer.FindInterfaceForHeader(datagramHeader)
	if err != nil {
		return fmt.Errorf("error finding interface for datagram header: %w", err)
	}
	if err := intf.SendTransportSegment(ctx, datagramHeader, segment); err != nil {
		return fmt.Errorf("error sending tcp synack segment: %w", err)
	}

	// receive ACK segment
	var peerAck uint32
	select {
	case <-ctx.Done():
		return ctx.Err()
	case peerAck = <-h.ack:
	}

	t.myAck = myAck
	t.peerAck = peerAck

	return nil
}

func (tcp) newConn(l *listener, remoteAddr addr, h handshake) conn {
	return &tcpConn{
		l:          l,
		remoteAddr: remoteAddr,
		h:          h,
	}
}

func (t *tcpConn) handshake(ctx context.Context) error {
	if handshake := t.h; handshake != nil {
		if err := handshake.do(ctx, t); err != nil {
			return err
		}
		t.h = nil
	}
	return nil
}

func (t *tcpConn) recv(segment gopacket.TransportLayer) {
	// forward to handshake first
	if handshake := t.h; handshake != nil {
		handshake.recv(segment)
		return
	}

	// TODO
}

func (t *tcpConn) Read(b []byte) (n int, err error) {
	return 0, nil // TODO
}

func (t *tcpConn) Write(b []byte) (n int, err error) {
	return 0, nil // TODO
}

func (t *tcpConn) Close() error {
	return nil // TODO
}

func (t *tcpConn) LocalAddr() net.Addr {
	return t.l.Addr()
}

func (t *tcpConn) RemoteAddr() net.Addr {
	a := t.remoteAddr
	return &a
}

// SetDeadline is the same as calling SetReadDeadline() and
// SetWriteDeadline().
func (t *tcpConn) SetDeadline(d time.Time) error {
	var err error
	if dErr := t.SetReadDeadline(d); dErr != nil {
		err = multierror.Append(err, fmt.Errorf("error setting read deadline: %w", err))
	}
	if dErr := t.SetWriteDeadline(d); dErr != nil {
		err = multierror.Append(err, fmt.Errorf("error setting write deadline: %w", err))
	}
	return err
}

func (t *tcpConn) SetReadDeadline(d time.Time) error {
	return nil // TODO
}

func (t *tcpConn) SetWriteDeadline(d time.Time) error {
	return nil // TODO
}
