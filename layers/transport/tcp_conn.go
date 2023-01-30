package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/matheuscscp/net-sim/observability"
	pkgcontext "github.com/matheuscscp/net-sim/pkg/context"
	pkgio "github.com/matheuscscp/net-sim/pkg/io"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/hashicorp/go-multierror"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/sirupsen/logrus"
)

type (
	tcpConn struct {
		ctx                context.Context
		cancelCtx          context.CancelFunc
		wg                 sync.WaitGroup
		listener           *listener
		remoteAddr         addr
		handshake          handshake
		handshakeErr       error
		handshakeCtx       context.Context
		cancelHandshakeCtx context.CancelFunc
		stateErr           error
		isServer           bool

		nextExpectedSeq  uint32
		readMu           sync.Mutex
		readCh           chan *gplayers.TCP // data segments buffer
		readDeadline     *deadline
		readBuf          []byte            // data ready to be returned
		readSeqCache     map[uint32][]byte // cached data mapped by sequence number
		readStreamClosed bool
		sendAckAsync     chan struct{} // event channel for triggering the send ack thread to send an ACK

		seedSeq            uint32
		nextSeq            uint32
		writeMu            sync.Mutex
		writeCh            chan uint32 // a stream of ack numbers
		writeDeadline      *deadline
		writeStreamClosed  bool
		closeWriteStreamMu sync.Mutex
		closeWriteStreamCh chan struct{}

		strayOrDelayedAckSegments prometheus.Counter
	}

	BidirectionalStream interface {
		CloseWrite() error
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

	_ BidirectionalStream = (*tcpConn)(nil)
)

func (tcpFactory) newConn(listener *listener, remoteAddr addr, handshake handshake) conn {
	ctx, cancel := context.WithCancel(context.Background())
	ctxDone := ctx.Done()
	handshakeCtx, cancelHandshakeCtx := context.WithCancel(context.Background())
	_, isServer := handshake.(*tcpServerHandshake)
	metricLabels := prometheus.Labels{
		observability.StackName: listener.protocol.layer.networkLayer.StackName(),
		labelNameLocalAddr:      listener.Addr().String(),
		labelNameRemoteAddr:     remoteAddr.String(),
	}
	t := &tcpConn{
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
		sendAckAsync: make(chan struct{}, channelSize),

		writeCh:            make(chan uint32, channelSize),
		writeDeadline:      newDeadline(),
		closeWriteStreamCh: make(chan struct{}),

		strayOrDelayedAckSegments: strayOrDelayedAckSegments.With(metricLabels),
	}

	// start send ack thread
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()

		sendAck := func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			return t.sendAckSegment(ctx)
		}

		delayAndSendAck := func() error {
			sendDelay := time.NewTimer(100 * time.Millisecond)
			defer sendDelay.Stop()
			select {
			// delay finished. time to send
			case <-sendDelay.C:
				return sendAck()
			// another send async ack was requested. reset delay
			case <-t.sendAckAsync:
				return errResetDelay
			// conn context done. send final ack
			case <-ctxDone:
				return sendAck()
			}
		}

		for {
			select {
			// process sendAck event
			case <-t.sendAckAsync:
				for err := delayAndSendAck(); err != nil; err = delayAndSendAck() {
					if errors.Is(err, errResetDelay) {
						continue
					}
					t.
						connLogger().
						WithError(err).
						Error("error sending async tcp ack segment")
					return
				}
			// context
			case <-ctxDone:
				return
			}
		}
	}()

	return t
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
	if t.handshakeErr = t.handshake.do(ctx, t); t.handshakeErr != nil {
		if pkgcontext.IsContextError(ctx, t.handshakeErr) {
			if !localCtxDone {
				t.handshakeErr = fmt.Errorf("(*tcpConn).doHandshake(ctx) done while doing handshake: %w", t.handshakeErr)
			}
			if tCtxDone {
				t.handshakeErr = fmt.Errorf("(*tcpConn).ctx done while doing handshake: %w", t.handshakeErr)
			}
			t.handshakeErr = fmt.Errorf("(*tcpConn).handshakeCtx done while doing handshake: %w", t.handshakeErr)
		}
		return t.handshakeErr
	}
	t.handshake = nil

	return nil
}

func (t *tcpConn) waitHandshake() (err error) {
	select {
	case <-t.handshakeCtx.Done():
		err = t.handshakeErr
	case <-t.ctx.Done():
		if t.handshakeErr != nil {
			err = t.handshakeErr
		} else {
			err = fmt.Errorf("(*tcpConn).ctx done while waiting for handshake to complete: %w", t.ctx.Err())
		}
	}
	if t.handshake == nil {
		err = nil
	}
	return
}

func (t *tcpConn) recv(segment gopacket.TransportLayer) {
	if t.ctx.Err() != nil || t.stateErr != nil {
		return
	}

	tcpSegment := segment.(*gplayers.TCP)

	// forward to handshake first
	if handshake := t.handshake; handshake != nil {
		handshake.recv(tcpSegment)
	} else if tcpSegment.SYN {
		if t.isServer && !tcpSegment.ACK && tcpSegment.Seq == t.nextExpectedSeq-1 {
			// this is a server and it's possible that the SYNACK segment sent in the handshake
			// got lost in the network, causing the client to retry the initial SYN segment.
			// reply SYNACK again
			if err := t.sendSynackSegment(t.ctx); err != nil &&
				t.stateErr == nil &&
				!pkgcontext.IsContextError(t.ctx, err) {
				const msg = "error resending tcp handshake synack segment"
				t.
					connLogger().
					WithError(err).
					WithField("syn_tcp_segment", tcpSegment).
					Error(msg)
				t.stateErr = fmt.Errorf("%s: %w", msg, err)
				t.closeInternalResourcesAndDeleteConnFromListener()
			}
		}
		return
	}

	// handle RST
	if tcpSegment.RST {
		t.stateErr = ErrConnReset
		t.closeInternalResourcesAndDeleteConnFromListener()
		return
	}

	// forward data and FINs to Read()
	if len(tcpSegment.Payload) > 0 || tcpSegment.FIN {
		select {
		case t.readCh <- tcpSegment:
		default:
		}
	}

	// forward ACKs to Write()
	if tcpSegment.ACK {
		// if the write stream on this side of the connection was
		// previously closed and the acknowledgement number matches
		// the next sequence number, then send another FIN segment
		t.closeWriteStreamMu.Lock()
		writeStreamClosed := t.writeStreamClosed
		t.closeWriteStreamMu.Unlock()
		if writeStreamClosed && tcpSegment.Ack == t.nextSeq {
			if err := t.sendFinSegment(); err != nil &&
				t.stateErr == nil &&
				!pkgcontext.IsContextError(t.ctx, err) {
				const msg = "error resending tcp fin segment"
				t.
					connLogger().
					WithError(err).
					WithField("ack_tcp_segment", tcpSegment).
					Error(msg)
			}
			return
		}

		select {
		case t.writeCh <- tcpSegment.Ack:
		default:
		}
	}
}

func (t *tcpConn) Read(b []byte) (int, error) {
	// lock read
	t.readMu.Lock()
	defer t.readMu.Unlock()

	// check read stream closed
	if t.readStreamClosed {
		return 0, io.EOF
	}

	// wait handshake
	if err := t.waitHandshake(); err != nil {
		return 0, err
	}

	// create deadline context
	var deadlineExceeded bool
	ctx, cancel := t.readDeadline.withContext(t.ctx, &deadlineExceeded)
	defer cancel()
	ctxDone := ctx.Done()

	// check state errors
	checkStateErrors := func() error {
		if deadlineExceeded {
			return ErrDeadlineExceeded
		}
		return t.stateErr
	}
	if err := checkStateErrors(); err != nil {
		return 0, err
	}
	if ctx.Err() != nil {
		return 0, fmt.Errorf("(*tcpConn).ctx done before reading bytes: %w", ctx.Err())
	}

	// if output buffer is empty return immediately
	if len(b) == 0 {
		return 0, nil
	}

	// prepare send ack helper
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

	// prepare helper to pull bytes from the readBuf and update the next expected sequence number
	pullFromReadBufAndUpdateNextExpectedSeq := func() (n int, err error) {
		// handle read stream closed
		if t.readStreamClosed {
			if err = sendAck(); err == nil {
				err = io.EOF
			}
			return
		}

		// pull
		if n = copy(b, t.readBuf); n == 0 {
			return
		}
		t.readBuf = t.readBuf[n:]
		if len(t.readBuf) == 0 {
			t.readBuf = nil
		}

		// update
		t.nextExpectedSeq = uint32(int64(t.nextExpectedSeq) + int64(n))
		select {
		case t.sendAckAsync <- struct{}{}:
		case <-ctxDone:
			if err = checkStateErrors(); err == nil {
				err = fmt.Errorf("(*tcpConn).ctx done while scheduling tcp ack segment to be sent: %w", err)
			}
		}
		return
	}

	// if some bytes are available in the readBuf return them right away
	if n, err := pullFromReadBufAndUpdateNextExpectedSeq(); n > 0 {
		return n, err
	}

	// prepare helper to update the readBuf. empty payload means that the segment has the FIN flag set
	updateReadBuf := func(payload []byte) {
		if len(payload) > 0 {
			t.readBuf = payload
			return
		}

		// handle FIN segments
		t.readStreamClosed = true
		t.nextExpectedSeq++
	}

	// if the next expected sequence number is already available in the cache, return the bytes right away and
	// send an ACK to the peer
	if dataSegmentPayload, ok := t.readSeqCache[t.nextExpectedSeq]; ok {
		delete(t.readSeqCache, t.nextExpectedSeq)
		updateReadBuf(dataSegmentPayload)
		return pullFromReadBufAndUpdateNextExpectedSeq()
	}

	// process data segments available in the read channel before sending an ACK to provoke the peer. the helper
	// function below adds the payload of segments whose sequence number do not match the next expected one in
	// the cache, and updates the readBuf otherwise
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

		updateReadBuf(dataSegment.Payload)
		return true
	}
	for done := false; !done; {
		select {
		// no more data segments available in the read channel
		default:
			done = true
		// process next data segment
		case dataSegment := <-t.readCh:
			if processDataSegment(dataSegment) {
				return pullFromReadBufAndUpdateNextExpectedSeq()
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

	// wait for events
	for backoffPowerOfTwo := 0; ; {
		retransmissionTimeout := time.NewTimer((1 << backoffPowerOfTwo) * time.Millisecond)
		defer retransmissionTimeout.Stop()
		select {
		// retransmission timeout. send another ACK to provoke peer to send the next data segment we are expecting
		case <-retransmissionTimeout.C:
			if err := sendAck(); err != nil {
				return 0, err
			}
			backoffPowerOfTwo++
		// data segment arrived
		case dataSegment := <-t.readCh:
			if processDataSegment(dataSegment) {
				return pullFromReadBufAndUpdateNextExpectedSeq()
			}
			// segment sequence number is not the next expected one. send another ACK and reset backoff
			backoffPowerOfTwo = 0
			if err := sendAck(); err != nil {
				return 0, err
			}
		// context
		case <-ctxDone:
			if err := checkStateErrors(); err != nil {
				return 0, err
			}
			return 0, fmt.Errorf("(*tcpConn).ctx done while waiting for tcp data segment: %w", ctx.Err())
		}
	}
}

func (t *tcpConn) Write(b []byte) (ackedBytes int, err error) {
	// lock write
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	// check write stream closed
	if t.writeStreamClosed {
		err = ErrWriteClosed
		return
	}

	// wait handshake
	if err = t.waitHandshake(); err != nil {
		return
	}

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
		return t.stateErr
	}
	if err = checkStateErrors(); err != nil {
		return
	}
	if ctx.Err() != nil {
		err = fmt.Errorf("(*tcpConn).ctx done before writing bytes: %w", ctx.Err())
		return
	}

	// if payload is empty return immediately
	numBytes := int64(len(b))
	if numBytes == 0 {
		return
	}

	// send loop
	defer func() { t.nextSeq += uint32(ackedBytes) }()
	for nextByte, backoffPowerOfTwo := int64(0), 0; int64(ackedBytes) < numBytes; {
		// calculate end of window
		endOfWindow := int64(ackedBytes) + TCPWindowSize
		if endOfWindow > numBytes {
			endOfWindow = numBytes
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
		retransmissionTimeout := time.NewTimer((1 << backoffPowerOfTwo) * time.Millisecond)
		defer retransmissionTimeout.Stop()
		select {
		// retransmission timeout. reset window
		case <-retransmissionTimeout.C:
			nextByte = int64(ackedBytes)
			backoffPowerOfTwo++
		// write stream was closed
		case <-t.closeWriteStreamCh:
			err = ErrWriteClosed
			return
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
			if !(0 <= delta && int64(ackedBytes)+delta <= endOfWindow) {
				t.strayOrDelayedAckSegments.Inc()
				continue
			}
			// consume valid non-negative delta
			backoffPowerOfTwo = 0
			ackedBytes += int(delta)
		// context
		case <-ctxDone:
			if err = checkStateErrors(); err != nil {
				return
			}
			err = fmt.Errorf("(*tcpConn).ctx done while waiting for tcp ack segment: %w", ctx.Err())
			return
		}
	}

	return
}

func (t *tcpConn) CloseWrite() error {
	// lock close write stream
	t.closeWriteStreamMu.Lock()
	defer t.closeWriteStreamMu.Unlock()

	// check already closed
	if t.writeStreamClosed {
		return nil
	}

	// close
	t.writeStreamClosed = true
	close(t.closeWriteStreamCh) // make Write() return (and run t.writeMu.Unlock())

	// lock write to make sure t.nextSeq has the latest acknowledged value
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	// send FIN
	if err := t.sendFinSegment(); err != nil {
		return fmt.Errorf("error sending tcp fin segment: %w", err)
	}

	return nil
}

func (t *tcpConn) Close() error {
	err := t.CloseWrite()
	t.closeInternalResourcesAndDeleteConnFromListener()
	return err
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

func (t *tcpConn) sendFinSegment() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	datagramHeader, segment := t.newDatagramHeaderAndSegment()
	segment.FIN = true
	segment.Seq = t.nextSeq
	return t.listener.protocol.layer.send(ctx, datagramHeader, segment)
}

func (t *tcpConn) closeInternalResourcesAndDeleteConnFromListener() {
	// cancel ctx and wait threads
	var cancel context.CancelFunc
	cancel, t.cancelCtx = t.cancelCtx, nil
	if cancel == nil {
		return
	}
	cancel()
	t.wg.Wait()

	// remove conn from listener so incoming segments are
	// not directed to this conn anymore
	t.listener.deleteConn(t.remoteAddr)

	// close deadlines
	pkgio.Close(t.readDeadline, t.writeDeadline)
}

func (t *tcpConn) connLogger() logrus.FieldLogger {
	return t.listener.
		connLogger(t).
		WithField("seed_seq", t.seedSeq)
}
