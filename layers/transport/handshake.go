package transport

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

func init() {
	rand.Seed(time.Now().UnixMilli())
}

type (
	handshake interface {
		recv(segment gopacket.TransportLayer)
		do(ctx context.Context, c conn) error
	}

	tcpClientHandshake struct {
		synack chan tcpSynAck
		ackrst chan struct{}
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
	return &tcpClientHandshake{
		synack: make(chan tcpSynAck, 1),
		ackrst: make(chan struct{}, 1),
	}
}

func (t *tcpClientHandshake) recv(segment gopacket.TransportLayer) {
	switch tcpSegment := segment.(*gplayers.TCP); {
	case tcpSegment.SYN && tcpSegment.ACK:
		select {
		case t.synack <- tcpSynAck{seq: tcpSegment.Seq, ack: tcpSegment.Ack}:
		default:
		}
	case tcpSegment.RST && tcpSegment.ACK:
		select {
		case t.ackrst <- struct{}{}:
		default:
		}
	}
}

func (t *tcpClientHandshake) do(ctx context.Context, c conn) error {
	tc := c.(*tcpConn)

	seq := rand.Uint32()
	var ack uint32
	err := retryWithBackoff(ctx, func(ctx context.Context) error {
		// send SYN segment
		datagramHeader, segment := tc.newDatagramHeaderAndSegment()
		segment.SYN = true
		segment.Seq = seq
		if err := tc.l.s.transportLayer.send(ctx, datagramHeader, segment); err != nil {
			return fmt.Errorf("error sending tcp syn segment: %w", err)
		}

		// receive SYNACK segment
		select {
		case <-ctx.Done():
			return fmt.Errorf("(*tcpClientHandshake).do(ctx) done while waiting for tcp synack segment: %w", ctx.Err())
		case synack := <-t.synack:
			if synack.ack != seq+1 {
				return fmt.Errorf("peer sent wrong ack number on handshake. want %d, got %d", seq+1, synack.ack)
			}
			seq++
			ack = synack.seq + 1
		case <-t.ackrst:
			return ErrConnReset
		}

		return nil
	})
	if err != nil {
		return err
	}

	// send ACK segment
	datagramHeader, segment := tc.newDatagramHeaderAndSegment()
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
	tcpSegment := segment.(*gplayers.TCP)
	switch {
	case tcpSegment.SYN && !tcpSegment.ACK:
		select {
		case t.syn <- tcpSegment.Seq:
		default:
		}
	case tcpSegment.ACK && !tcpSegment.SYN:
		select {
		case t.ack <- tcpSegment.Ack:
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
		return fmt.Errorf("(*tcpServerHandshake).do(ctx) done while waiting for tcp syn segment: %w", ctx.Err())
	case seq := <-t.syn:
		ack = seq + 1
	}

	seq := rand.Uint32()
	err := retryWithBackoff(ctx, func(ctx context.Context) error {
		// send SYNACK segment
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
			return fmt.Errorf("(*tcpServerHandshake).do(ctx) done while waiting for tcp ack segment: %w", ctx.Err())
		case ack := <-t.ack:
			if ack != seq+1 {
				return fmt.Errorf("peer sent wrong ack number on handshake. want %d, got %d", seq+1, ack)
			}
			seq++
		}

		return nil
	})
	if err != nil {
		return err
	}

	tc.seq = seq
	tc.ack = ack

	return nil
}

func (tcp) shouldCreatePendingConn(segment gopacket.TransportLayer) bool {
	t := segment.(*gplayers.TCP)
	return t != nil && t.SYN && !t.ACK
}

func (udp) newClientHandshake() handshake {
	return nil // no-op
}

func (udp) newServerHandshake() handshake {
	return nil // no-op
}

func (udp) shouldCreatePendingConn(segment gopacket.TransportLayer) bool {
	return true
}
