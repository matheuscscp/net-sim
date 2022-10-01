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

func (t *tcpClientHandshake) recv(segment gopacket.TransportLayer) {
	s := segment.(*gplayers.TCP)
	switch {
	case s.SYN && s.ACK:
		select {
		case t.synack <- tcpSynAck{seq: s.Seq, ack: s.Ack}:
		default:
		}
	}
}

func (t *tcpClientHandshake) do(ctx context.Context, c conn) error {
	tc := c.(*tcpConn)

	// send SYN segment
	seq := rand.Uint32()
	datagramHeader, segment := tc.newDatagramHeaderAndSegment()
	segment.SYN = true
	segment.Seq = seq
	if err := tc.l.s.transportLayer.send(ctx, datagramHeader, segment); err != nil {
		return fmt.Errorf("error sending tcp syn segment: %w", err)
	}

	// receive SYNACK segment
	var ack uint32
	select {
	case <-ctx.Done():
		return ctx.Err()
	case synack := <-t.synack:
		if synack.ack != seq+1 {
			return fmt.Errorf("peer sent wrong ack number on handshake. want %d, got %d", seq+1, synack.ack)
		}
		seq++
		ack = synack.seq + 1
	}

	// send ACK segment
	datagramHeader, segment = tc.newDatagramHeaderAndSegment()
	segment.ACK = true
	segment.Ack = ack
	if err := tc.l.s.transportLayer.send(ctx, datagramHeader, segment); err != nil {
		return fmt.Errorf("error sending tcp ack segment: %w", err)
	}

	tc.seq = seq
	tc.ack = ack

	return nil
}

func (tcp) newServerHandshake() handshake {
	return &tcpServerHandshake{make(chan uint32, 1), make(chan uint32, 1)}
}

func (t *tcpServerHandshake) recv(segment gopacket.TransportLayer) {
	s := segment.(*gplayers.TCP)
	switch {
	case s.SYN && !s.ACK:
		select {
		case t.syn <- s.Seq:
		default:
		}
	case s.ACK && !s.SYN:
		select {
		case t.ack <- s.Ack:
		default:
		}
	}
}

func (t *tcpServerHandshake) do(ctx context.Context, c conn) error {
	tc := c.(*tcpConn)

	// receive SYN segment
	var ack uint32
	select {
	case <-ctx.Done():
		return ctx.Err()
	case seq := <-t.syn:
		ack = seq + 1
	}

	// send SYNACK segment
	seq := rand.Uint32()
	datagramHeader, segment := tc.newDatagramHeaderAndSegment()
	segment.SYN = true
	segment.Seq = seq
	segment.ACK = true
	segment.Ack = ack
	if err := tc.l.s.transportLayer.send(ctx, datagramHeader, segment); err != nil {
		return fmt.Errorf("error sending tcp synack segment: %w", err)
	}

	// receive ACK segment
	select {
	case <-ctx.Done():
		return ctx.Err()
	case ack := <-t.ack:
		if ack != seq+1 {
			return fmt.Errorf("peer sent wrong ack number on handshake. want %d, got %d", seq+1, ack)
		}
		seq++
	}

	tc.seq = seq
	tc.ack = ack

	return nil
}

func (udp) newClientHandshake() handshake {
	return nil // no-op
}

func (udp) newServerHandshake() handshake {
	return nil // no-op
}
