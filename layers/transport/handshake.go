package transport

import (
	"context"
	"fmt"
	"math/rand"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

type (
	handshake interface {
		recv(segment gopacket.TransportLayer)
		do(ctx context.Context, c conn) error
	}

	tcpClientHandshake struct {
		synack chan tcpSynAck
	}

	tcpServerHandshake struct {
		syn chan uint32
		ack chan uint32
	}

	tcpSynAck struct {
		seq, ack uint32
	}
)

func (tcp) newClientHandshake() handshake {
	return &tcpClientHandshake{make(chan tcpSynAck, 1)}
}

func (h *tcpClientHandshake) recv(segment gopacket.TransportLayer) {
	s := segment.(*gplayers.TCP)
	switch {
	case s.SYN && s.ACK:
		select {
		case h.synack <- tcpSynAck{seq: s.Seq, ack: s.Ack}:
		default:
		}
	}
}

func (h *tcpClientHandshake) do(ctx context.Context, c conn) error {
	t := c.(*tcpConn)

	// send SYN segment
	seq := rand.Uint32()
	datagramHeader, segment := t.newDatagramHeaderAndSegment()
	segment.SYN = true
	segment.Seq = seq
	if err := t.l.s.transportLayer.send(ctx, datagramHeader, segment); err != nil {
		return fmt.Errorf("error sending tcp syn segment: %w", err)
	}

	// receive SYNACK segment
	var ack uint32
	select {
	case <-ctx.Done():
		return ctx.Err()
	case synack := <-h.synack:
		if synack.ack != seq+1 {
			return fmt.Errorf("peer sent wrong ack number on handshake. want %d, got %d", seq+1, synack.ack)
		}
		seq++
		ack = synack.seq + 1
	}

	// send ACK segment
	datagramHeader, segment = t.newDatagramHeaderAndSegment()
	segment.ACK = true
	segment.Ack = ack
	if err := t.l.s.transportLayer.send(ctx, datagramHeader, segment); err != nil {
		return fmt.Errorf("error sending tcp ack segment: %w", err)
	}

	t.seq = seq
	t.ack = ack

	return nil
}

func (tcp) newServerHandshake() handshake {
	return &tcpServerHandshake{make(chan uint32, 1), make(chan uint32, 1)}
}

func (h *tcpServerHandshake) recv(segment gopacket.TransportLayer) {
	s := segment.(*gplayers.TCP)
	switch {
	case s.SYN && !s.ACK:
		select {
		case h.syn <- s.Seq:
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
	var ack uint32
	select {
	case <-ctx.Done():
		return ctx.Err()
	case seq := <-h.syn:
		ack = seq + 1
	}

	// send SYNACK segment
	seq := rand.Uint32()
	datagramHeader, segment := t.newDatagramHeaderAndSegment()
	segment.SYN = true
	segment.Seq = seq
	segment.ACK = true
	segment.Ack = ack
	if err := t.l.s.transportLayer.send(ctx, datagramHeader, segment); err != nil {
		return fmt.Errorf("error sending tcp synack segment: %w", err)
	}

	// receive ACK segment
	select {
	case <-ctx.Done():
		return ctx.Err()
	case ack := <-h.ack:
		if ack != seq+1 {
			return fmt.Errorf("peer sent wrong ack number on handshake. want %d, got %d", seq+1, ack)
		}
		seq++
	}

	t.seq = seq
	t.ack = ack

	return nil
}

func (udp) newClientHandshake() handshake {
	return nil // no-op
}

func (udp) newServerHandshake() handshake {
	return nil // no-op
}
