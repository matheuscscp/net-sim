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
	case tcpSegment.ACK && tcpSegment.RST:
		select {
		case t.ackrst <- struct{}{}:
		default:
		}
	}
}

func (t *tcpClientHandshake) do(ctx context.Context, c conn) error {
	tc := c.(*tcpConn)

	// choose a fixed random initial sequence number and send it on a retry loop
	clientSeq := rand.Uint32()
	for backoffPowerOfTwo := 0; ; backoffPowerOfTwo++ {
		// send SYN segment
		datagramHeader, segment := tc.newDatagramHeaderAndSegment()
		segment.SYN = true
		segment.Seq = clientSeq
		if err := tc.l.s.transportLayer.send(ctx, datagramHeader, segment); err != nil {
			return fmt.Errorf("error sending tcp syn segment: %w", err)
		}

		// wait for SYNACK segment
		retransmissionTimeout := time.NewTimer((1 << backoffPowerOfTwo) * time.Second)
		defer retransmissionTimeout.Stop()
		select {
		// retransmission timeout
		case <-retransmissionTimeout.C:
			continue
		// the server host is up but not listening at the dst port
		case <-t.ackrst:
			return ErrConnReset
		// context canceled or deadline exceeded
		case <-ctx.Done():
			return fmt.Errorf("(*tcpClientHandshake).do(ctx) done while waiting for tcp synack segment: %w", ctx.Err())
		// SYNACK segment arrived
		case synack := <-t.synack:
			// reset connection upon wrong ack number
			if synack.ack != clientSeq+1 {
				err := fmt.Errorf("server sent wrong ack number, want %v, got %v. connection will be reset", clientSeq+1, synack.ack)
				if sendErr := tc.sendAckrstSegment(ctx); sendErr != nil {
					return fmt.Errorf("%w (error sending tcp ackrst segment: %v)", err, sendErr)
				}
				return err
			}

			// store seq and ack in the connection
			tc.seq = clientSeq + 1
			tc.ack = synack.seq + 1

			// send ACK segment
			if err := tc.sendAckSegment(ctx); err != nil {
				return fmt.Errorf("error sending tcp ack segment: %w", err)
			}

			return nil
		}
	}
}

func (tcp) newServerHandshake() handshake {
	return &tcpServerHandshake{
		syn: make(chan uint32, 1),
	}
}

func (t *tcpServerHandshake) recv(segment gopacket.TransportLayer) {
	tcpSegment := segment.(*gplayers.TCP)
	switch {
	case tcpSegment.SYN && !tcpSegment.ACK:
		select {
		case t.syn <- tcpSegment.Seq:
		default:
		}
	}
}

func (t *tcpServerHandshake) do(ctx context.Context, c conn) error {
	tc := c.(*tcpConn)

	// consume SYN segment and store ack in the connection
	select {
	case <-ctx.Done():
		return fmt.Errorf("(*tcpServerHandshake).do(ctx) done while consuming tcp syn segment: %w", ctx.Err())
	case clientSeq := <-t.syn:
		tc.ack = clientSeq + 1
	}

	// choose a random initial sequence number and send it on a SYNACK segment
	serverSeq := rand.Uint32()
	datagramHeader, segment := tc.newDatagramHeaderAndSegment()
	segment.SYN = true
	segment.Seq = serverSeq
	segment.ACK = true
	segment.Ack = tc.ack
	if err := tc.l.s.transportLayer.send(ctx, datagramHeader, segment); err != nil {
		return fmt.Errorf("error sending tcp synack segment: %w", err)
	}

	// we dont block waiting for the third-way ACK from the client
	// because it may get lost in the network and the client should
	// not retry anyway. it's better to consider the handshake done
	// and unblock the server from sending data to the client, so the
	// client can have more chances to send ACKs. if a real problem
	// happened during the handshake on the client side and the
	// client really did not receive the seq number above, then the
	// tcp connection logic will detect the problem and reset the
	// connection anyway, otherwise everything is fine
	tc.seq = serverSeq + 1
	return nil
}

func (udp) newClientHandshake() handshake {
	return nil // no-op
}

func (udp) newServerHandshake() handshake {
	return nil // no-op
}
