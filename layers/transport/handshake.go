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
		do(ctx context.Context, conn conn) error
	}

	tcpClientHandshake struct {
		serverSynack chan tcpSynAck
		serverAckrst chan struct{}
	}

	tcpServerHandshake struct {
		clientSeq chan uint32
	}

	tcpSynAck struct {
		seq, ack uint32
	}
)

func (tcpFactory) newClientHandshake() handshake {
	return &tcpClientHandshake{
		serverSynack: make(chan tcpSynAck, 1),
		serverAckrst: make(chan struct{}, 1),
	}
}

func (client *tcpClientHandshake) recv(segment gopacket.TransportLayer) {
	switch serverSegment := segment.(*gplayers.TCP); {
	case serverSegment.SYN && serverSegment.ACK:
		select {
		case client.serverSynack <- tcpSynAck{seq: serverSegment.Seq, ack: serverSegment.Ack}:
		default:
		}
	case serverSegment.ACK && serverSegment.RST:
		select {
		case client.serverAckrst <- struct{}{}:
		default:
		}
	}
}

func (client *tcpClientHandshake) do(ctx context.Context, conn conn) error {
	t := conn.(*tcpConn)

	// choose a fixed random initial sequence number and send it on a retry loop
	clientSeq := rand.Uint32()
	for backoffPowerOfTwo := 0; ; backoffPowerOfTwo++ {
		// send SYN segment
		datagramHeader, segment := t.newDatagramHeaderAndSegment()
		segment.SYN = true
		segment.Seq = clientSeq
		if err := t.listener.protocol.layer.send(ctx, datagramHeader, segment); err != nil {
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
		case <-client.serverAckrst:
			return ErrConnReset
		// context canceled or deadline exceeded
		case <-ctx.Done():
			return fmt.Errorf("(*tcpClientHandshake).do(ctx) done while waiting for tcp synack segment: %w", ctx.Err())
		// SYNACK segment arrived
		case serverSynack := <-client.serverSynack:
			// reset connection upon wrong ack number
			if serverSynack.ack != clientSeq+1 {
				err := fmt.Errorf("server sent wrong ack number, want %v, got %v. connection will be reset", clientSeq+1, serverSynack.ack)
				if sendErr := t.sendAckrstSegment(ctx); sendErr != nil {
					return fmt.Errorf("%w (error sending tcp ackrst segment: %v)", err, sendErr)
				}
				return err
			}

			// store seq and ack in the connection
			t.seq = clientSeq + 1
			t.ack = serverSynack.seq + 1

			// send ACK segment
			if err := t.sendAckSegment(ctx); err != nil {
				return fmt.Errorf("error sending tcp ack segment: %w", err)
			}

			return nil
		}
	}
}

func (tcpFactory) newServerHandshake() handshake {
	return &tcpServerHandshake{
		clientSeq: make(chan uint32, 1),
	}
}

func (server *tcpServerHandshake) recv(segment gopacket.TransportLayer) {
	switch clientSegment := segment.(*gplayers.TCP); {
	case clientSegment.SYN && !clientSegment.ACK:
		select {
		case server.clientSeq <- clientSegment.Seq:
		default:
		}
	}
}

func (server *tcpServerHandshake) do(ctx context.Context, conn conn) error {
	t := conn.(*tcpConn)

	// consume SYN segment and store ack in the connection
	select {
	case <-ctx.Done():
		return fmt.Errorf("(*tcpServerHandshake).do(ctx) done while consuming tcp syn segment: %w", ctx.Err())
	case clientSeq := <-server.clientSeq:
		t.ack = clientSeq + 1
	}

	// choose a random initial sequence number and send it on a SYNACK segment
	serverSeq := rand.Uint32()
	datagramHeader, segment := t.newDatagramHeaderAndSegment()
	segment.SYN = true
	segment.Seq = serverSeq
	segment.ACK = true
	segment.Ack = t.ack
	if err := t.listener.protocol.layer.send(ctx, datagramHeader, segment); err != nil {
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
	t.seq = serverSeq + 1
	return nil
}

func (udpFactory) newClientHandshake() handshake {
	return nil // no-op
}

func (udpFactory) newServerHandshake() handshake {
	return nil // no-op
}
