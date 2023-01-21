package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/matheuscscp/net-sim/layers/common"
	"github.com/matheuscscp/net-sim/observability"
	pkgcontext "github.com/matheuscscp/net-sim/pkg/context"
	pkgio "github.com/matheuscscp/net-sim/pkg/io"
	"github.com/sirupsen/logrus"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/hashicorp/go-multierror"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type (
	tcpConn struct {
		ctx                context.Context
		cancelCtx          context.CancelFunc
		listener           *listener
		remoteAddr         addr
		handshake          handshake
		handshakeCtx       context.Context
		cancelHandshakeCtx context.CancelFunc
		connClosed         bool
		connReset          bool
		isServer           bool

		nextExpectedSeq uint32
		readMu          sync.Mutex
		readCh          chan *gplayers.TCP // data segments buffer
		readDeadline    *deadline
		readBuf         []byte            // data ready to be returned
		readSeqCache    map[uint32][]byte // cached data mapped by sequence number

		seedSeq       uint32
		nextSeq       uint32
		writeMu       sync.Mutex
		writeCh       chan uint32 // a stream of ack numbers
		writeDeadline *deadline

		strayOrDelayedAckSegments prometheus.Counter
	}
)

const (
	promSubsystemTCPConn = "tcp_conn"
	labelNameLocalAddr   = "local_addr"
	labelNameRemoteAddr  = "remote_addr"
)

var (
	metricLabelsTCPConn = []string{
		observability.StackName,
		labelNameLocalAddr,
		labelNameRemoteAddr,
	}
	strayOrDelayedAckSegments = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: promNamespace,
		Subsystem: promSubsystemTCPConn,
		Name:      "stray_or_delayed_ack_segments",
		Help:      "Total number of stray or delayed ACK segments.",
	}, metricLabelsTCPConn)
)

func (tcpFactory) newConn(listener *listener, remoteAddr addr, handshake handshake) conn {
	ctx, cancel := context.WithCancel(context.Background())
	handshakeCtx, cancelHandshakeCtx := context.WithCancel(context.Background())
	_, isServer := handshake.(*tcpServerHandshake)
	metricLabels := prometheus.Labels{
		observability.StackName: listener.protocol.layer.networkLayer.StackName(),
		labelNameLocalAddr:      listener.Addr().String(),
		labelNameRemoteAddr:     remoteAddr.String(),
	}
	return &tcpConn{
		ctx:                ctx,
		cancelCtx:          cancel,
		listener:           listener,
		remoteAddr:         remoteAddr,
		handshake:          handshake,
		handshakeCtx:       handshakeCtx,
		cancelHandshakeCtx: cancelHandshakeCtx,
		isServer:           isServer,

		readCh:       make(chan *gplayers.TCP, channelSize),
		readDeadline: newDeadline(),
		readSeqCache: make(map[uint32][]byte),

		writeCh:       make(chan uint32, channelSize),
		writeDeadline: newDeadline(),

		strayOrDelayedAckSegments: strayOrDelayedAckSegments.With(metricLabels),
	}
}

func (t *tcpConn) doHandshake(ctx context.Context) error {
	if t.handshakeCtx.Err() != nil {
		return errors.New("(*tcpConn).doHandshake() already ran previously for this connection")
	}
	defer t.cancelHandshakeCtx()

	// merge local contexts
	var tCtxDone bool
	localCtx, cancelLocalCtx := pkgcontext.WithCancelOnAnotherContext(t.handshakeCtx, t.ctx, &tCtxDone)
	defer cancelLocalCtx()

	// merge func and local context
	var localCtxDone bool
	ctx, cancel := pkgcontext.WithCancelOnAnotherContext(ctx, localCtx, &localCtxDone)
	defer cancel()

	// do handshake
	if err := t.handshake.do(ctx, t); err != nil {
		if pkgcontext.IsContextError(ctx, err) {
			if !localCtxDone {
				return fmt.Errorf("(*tcpConn).doHandshake(ctx) done while doing handshake: %w", err)
			}
			if tCtxDone {
				return fmt.Errorf("(*tcpConn).ctx done while doing handshake: %w", err)
			}
			return fmt.Errorf("(*tcpConn).handshakeCtx done while doing handshake: %w", err)
		}
		return err
	}
	t.handshake = nil

	return nil
}

func (t *tcpConn) waitHandshake() error {
	var err error
	select {
	case <-t.handshakeCtx.Done():
		err = fmt.Errorf("(*tcpConn).handshakeCtx done while waiting for handshake to complete: %w", t.handshakeCtx.Err())
	case <-t.ctx.Done():
		err = fmt.Errorf("(*tcpConn).ctx done while waiting for handshake to complete: %w", t.ctx.Err())
	}
	if t.handshake == nil {
		return nil
	}
	return err
}

func (t *tcpConn) recv(segment gopacket.TransportLayer) {
	tcpSegment := segment.(*gplayers.TCP)

	// forward to handshake first
	if handshake := t.handshake; handshake != nil {
		handshake.recv(tcpSegment)
	} else if t.isServer && tcpSegment.SYN && !tcpSegment.ACK && tcpSegment.Seq == t.nextExpectedSeq-1 {
		// this is a server and it's possible that the SYNACK segment sent in the handshake
		// got lost in the network, causing the client to retry the initial SYN segment.
		// reply SYNACK again
		if err := t.sendSynackSegment(t.ctx); err != nil {
			logrus.
				WithError(err).
				WithField("syn_tcp_segment", tcpSegment).
				WithField("server_seed_seq", t.seedSeq).
				Error("error sending retry tcp synack segment")
			t.closeInternalResourcesAndDeleteConn()
		}
		return
	}

	// handle end of connection
	if tcpSegment.FIN {
		t.connClosed = true
		t.Close()
		return
	}
	if tcpSegment.RST {
		t.connReset = true
		t.closeInternalResourcesAndDeleteConn()
		return
	}

	// forward data to Read()
	if len(tcpSegment.Payload) > 0 {
		select {
		case t.readCh <- tcpSegment:
		default:
		}
	}

	// forward ACKs to Write()
	if tcpSegment.ACK {
		select {
		case t.writeCh <- tcpSegment.Ack:
		default:
		}
	}
}

func (t *tcpConn) Read(b []byte) (int, error) {
	if err := t.waitHandshake(); err != nil {
		return 0, err
	}

	t.readMu.Lock()
	defer t.readMu.Unlock()

	// create deadline context
	var deadlineExceeded bool
	ctx, cancel := t.readDeadline.withContext(t.ctx, &deadlineExceeded)
	defer cancel()

	// check state errors
	checkStateErrors := func() error {
		if deadlineExceeded {
			return ErrDeadlineExceeded
		}
		if t.connClosed {
			return ErrConnClosed
		}
		if t.connReset {
			return ErrConnReset
		}
		return nil
	}
	if err := checkStateErrors(); err != nil {
		return 0, err
	}
	if ctx.Err() != nil {
		return 0, fmt.Errorf("(*tcpConn).ctx done before reading bytes: %w", ctx.Err())
	}

	// if some bytes are still available from previous calls, return them right away
	if n := t.pullFromReadBuf(b); n > 0 {
		return n, nil
	}

	// prepare send ACK helper
	sendAck := func() error {
		if err := t.sendAckSegment(ctx); err != nil {
			if err := checkStateErrors(); err != nil {
				return err
			}
			if pkgcontext.IsContextError(ctx, err) {
				return fmt.Errorf("(*tcpConn).ctx done while sending tcp ack segment: %w", err)
			}
			return fmt.Errorf("error sending tcp ack segment: %w", err)
		}
		return nil
	}

	// if the next expected sequence number is already available in the cache, return the bytes right away and
	// send an ACK to the peer
	if newReadBuf, ok := t.readSeqCache[t.nextExpectedSeq]; ok {
		delete(t.readSeqCache, t.nextExpectedSeq)
		t.setReadBuf(newReadBuf)
		return t.pullFromReadBuf(b), sendAck()
	}

	// process data segments already available in the read channel before sending an ACK to provoke the peer,
	// adding the payload of segments whose sequence number do not match the next expected one to the cache
	processDataSegment := func(dataSegment *gplayers.TCP) bool {
		// sequence number does not match the next expected sequence number.
		// this might be a future segment, so just cache it for now. make
		// room in the cache if necessary
		if seq := dataSegment.Seq; seq != t.nextExpectedSeq {
			if len(t.readSeqCache) == tcpMaxReadCacheItems {
				for k := range t.readSeqCache {
					delete(t.readSeqCache, k)
					break
				}
			}
			t.readSeqCache[seq] = dataSegment.Payload
			return false
		}

		t.setReadBuf(dataSegment.Payload)
		return true
	}
	ctxDone := ctx.Done()
	for done := false; !done; {
		select {
		// no more data segments available in the read channel
		default:
			done = true
		// process next data segment
		case dataSegment := <-t.readCh:
			if processDataSegment(dataSegment) {
				return t.pullFromReadBuf(b), sendAck()
			}
		// context done
		case <-ctxDone:
			if err := checkStateErrors(); err != nil {
				return 0, err
			}
			return 0, fmt.Errorf("(*tcpConn).ctx done while processing available tcp data segments: %w", ctx.Err())
		}
	}

	// send ACK segment to provoke peer to send the next data segment that we are expecting
	if err := sendAck(); err != nil {
		return 0, err
	}

	// block waiting for events
	for backoffPowerOfTwo := 7; ; backoffPowerOfTwo++ {
		retransmissionTimeout := time.NewTimer((1 << backoffPowerOfTwo) * time.Millisecond)
		defer retransmissionTimeout.Stop()
		select {
		// retransmission timeout. send another ACK to provoke peer to send the next data segment we are expecting
		case <-retransmissionTimeout.C:
			if err := sendAck(); err != nil {
				return 0, err
			}
		// context
		case <-ctxDone:
			if err := checkStateErrors(); err != nil {
				return 0, err
			}
			return 0, fmt.Errorf("(*tcpConn).ctx done while waiting for tcp data segment: %w", ctx.Err())
		// data segment arrived
		case dataSegment := <-t.readCh:
			if processDataSegment(dataSegment) {
				return t.pullFromReadBuf(b), sendAck()
			}
		}
	}
}

func (t *tcpConn) Write(b []byte) (ackedBytes int, err error) {
	if err := t.waitHandshake(); err != nil {
		return 0, err
	}

	// validate payload size
	nBytes := int64(len(b))
	if nBytes == 0 {
		return 0, common.ErrCannotSendEmpty
	}

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	// create deadline context
	var deadlineExceeded bool
	ctx, cancel := t.writeDeadline.withContext(t.ctx, &deadlineExceeded)
	defer cancel()
	ctxDone := ctx.Done()

	// check state errors
	checkStateErrors := func() error {
		if deadlineExceeded {
			return ErrDeadlineExceeded
		}
		if t.connClosed {
			return ErrConnClosed
		}
		if t.connReset {
			return ErrConnReset
		}
		return nil
	}
	if err := checkStateErrors(); err != nil {
		return 0, err
	}
	if ctx.Err() != nil {
		return 0, fmt.Errorf("(*tcpConn).ctx done before writing bytes: %w", ctx.Err())
	}

	// send loop
	defer func() { t.nextSeq += uint32(ackedBytes) }()
	backoffPowerOfTwo := 6
	resetBackoff := func() { backoffPowerOfTwo -= (backoffPowerOfTwo - 6) >> 1 }
	for nextByte := int64(0); int64(ackedBytes) < nBytes; {
		// calculate end of window
		endOfWindow := int64(ackedBytes) + TCPWindowSize
		if endOfWindow > nBytes {
			endOfWindow = nBytes
		}

		// send from nextByte (inclusive) up to endOfWindow (exclusive)
		for nextByte < endOfWindow {
			// calculate end of segment payload
			endOfPayload := nextByte + TCPMTU
			if endOfPayload > endOfWindow {
				endOfPayload = endOfWindow
			}

			// send segment
			datagramHeader, segment := t.newDatagramHeaderAndSegment()
			segment.Seq = t.nextSeq + uint32(nextByte)
			segment.Payload = b[nextByte:endOfPayload]
			if err = t.listener.protocol.layer.send(ctx, datagramHeader, segment); err != nil {
				switch stateErr := checkStateErrors(); {
				case stateErr != nil:
					err = stateErr
				case pkgcontext.IsContextError(ctx, err):
					err = fmt.Errorf("(*tcpConn).ctx done while sending tcp data segment: %w", err)
				default:
					err = fmt.Errorf("error sending tcp data segment: %w", err)
				}
				return
			}

			nextByte = endOfPayload
		}

		// wait for events
		err = func() error {
			backoffPowerOfTwo++
			retransmissionTimeout := time.NewTimer((1 << backoffPowerOfTwo) * time.Millisecond)
			defer retransmissionTimeout.Stop()
			select {
			// retransmission timeout. reset window
			case <-retransmissionTimeout.C:
				nextByte = int64(ackedBytes)
			// context
			case <-ctxDone:
				if err := checkStateErrors(); err != nil {
					return err
				}
				return fmt.Errorf("(*tcpConn).ctx done while waiting for tcp ack segment: %w", ctx.Err())
			// ACK segment arrived
			case ack := <-t.writeCh:
				seq := int64(t.nextSeq) + int64(ackedBytes)
				delta := int64(ack) - seq
				if delta < 0 {
					// delta is negative. the only valid way for this to happen is
					// if the ack number just wrapped around the uint32 limit. in
					// this case we add back the lost 2^32 part
					delta += (int64(1) << 32)
				}
				if delta > 2*TCPWindowSize {
					t.strayOrDelayedAckSegments.Inc()
					return nil
				}
				// process valid positive delta
				resetBackoff()
				ackedBytes += int(delta)
				if int(nextByte) < ackedBytes {
					nextByte = int64(ackedBytes)
				}
			}
			return nil
		}()
		if err != nil {
			return
		}
	}

	return
}

func (t *tcpConn) Close() error {
	if !t.closeInternalResourcesAndDeleteConn() {
		return nil
	}

	// send FIN segment if connection was established
	if t.handshake == nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		finDatagramHeader, finSegment := t.newDatagramHeaderAndSegment()
		finSegment.FIN = true
		finSegment.Seq = t.nextSeq
		if err := t.listener.protocol.layer.send(ctx, finDatagramHeader, finSegment); err != nil {
			return fmt.Errorf("error sending tcp fin segment: %w", err)
		}
	}

	return nil
}

func (t *tcpConn) LocalAddr() net.Addr {
	return t.listener.Addr()
}

func (t *tcpConn) RemoteAddr() net.Addr {
	return newTCPAddr(t.remoteAddr)
}

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
	t.readDeadline.set(d)
	return nil
}

func (t *tcpConn) SetWriteDeadline(d time.Time) error {
	t.writeDeadline.set(d)
	return nil
}

// general helpers

func (t *tcpConn) newDatagramHeaderAndSegment() (*gplayers.IPv4, *gplayers.TCP) {
	datagramHeader := &gplayers.IPv4{
		DstIP:    t.remoteAddr.ipAddress.Raw(),
		Protocol: gplayers.IPProtocolTCP,
	}
	if t.listener.ipAddress != nil {
		datagramHeader.SrcIP = t.listener.ipAddress.Raw()
	}
	segment := &gplayers.TCP{
		DstPort: gplayers.TCPPort(t.remoteAddr.port),
		SrcPort: gplayers.TCPPort(t.listener.port),
		Window:  TCPWindowSize,
	}
	return datagramHeader, segment
}

func (t *tcpConn) sendAckSegment(ctx context.Context) error {
	datagramHeader, segment := t.newDatagramHeaderAndSegment()
	segment.ACK = true
	segment.Ack = t.nextExpectedSeq
	return t.listener.protocol.layer.send(ctx, datagramHeader, segment)
}

func (t *tcpConn) sendAckrstSegment(ctx context.Context) error {
	datagramHeader, segment := t.newDatagramHeaderAndSegment()
	segment.ACK = true
	segment.RST = true
	return t.listener.protocol.layer.send(ctx, datagramHeader, segment)
}

func (t *tcpConn) sendSynackSegment(ctx context.Context) error {
	datagramHeader, segment := t.newDatagramHeaderAndSegment()
	segment.SYN = true
	segment.Seq = t.seedSeq
	segment.ACK = true
	segment.Ack = t.nextExpectedSeq
	return t.listener.protocol.layer.send(ctx, datagramHeader, segment)
}

func (t *tcpConn) closeInternalResourcesAndDeleteConn() bool {
	// cancel ctx
	var cancel context.CancelFunc
	cancel, t.cancelCtx = t.cancelCtx, nil
	if cancel == nil {
		return false
	}
	cancel()

	// remove conn from listener so incoming segments are
	// not directed to this conn anymore
	t.listener.deleteConn(t.remoteAddr)

	// close deadlines
	pkgio.Close(t.readDeadline, t.writeDeadline)

	return true
}

// read helpers

func (t *tcpConn) setReadBuf(newReadBuf []byte) {
	t.readBuf = newReadBuf
	t.nextExpectedSeq += uint32(len(newReadBuf))
}

func (t *tcpConn) pullFromReadBuf(b []byte) int {
	n := copy(b, t.readBuf)
	t.readBuf = t.readBuf[n:]
	if len(t.readBuf) == 0 {
		t.readBuf = nil
	}
	return n
}
