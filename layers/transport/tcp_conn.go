package transport

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/matheuscscp/net-sim/layers/common"
	"github.com/matheuscscp/net-sim/observability"
	pkgcontext "github.com/matheuscscp/net-sim/pkg/context"
	pkgio "github.com/matheuscscp/net-sim/pkg/io"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/hashicorp/go-multierror"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type (
	tcpConn struct {
		ctx        context.Context
		cancelCtx  context.CancelFunc
		l          *listener
		remoteAddr addr
		h          handshake
		hCtx       context.Context

		ack          uint32
		readMu       sync.Mutex
		readCh       chan *gplayers.TCP // non acked segments buffer
		readDeadline *deadline
		readBuf      []byte            // acked data ready to be returned
		readCache    map[uint32][]byte // (non acked) cached segments mapped by sequence number

		seq           uint32
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

func (tcp) newConn(l *listener, remoteAddr addr, h handshake) conn {
	ctx, cancel := context.WithCancel(context.Background())
	metricLabels := prometheus.Labels{
		observability.StackName: l.s.transportLayer.networkLayer.StackName(),
		labelNameLocalAddr:      l.Addr().String(),
		labelNameRemoteAddr:     remoteAddr.String(),
	}
	return &tcpConn{
		ctx:        ctx,
		cancelCtx:  cancel,
		l:          l,
		remoteAddr: remoteAddr,
		h:          h,

		readCh:       make(chan *gplayers.TCP, channelSize),
		readDeadline: newDeadline(),
		readCache:    make(map[uint32][]byte),

		writeCh:       make(chan uint32, channelSize),
		writeDeadline: newDeadline(),

		strayOrDelayedAckSegments: strayOrDelayedAckSegments.With(metricLabels),
	}
}

func (t *tcpConn) setHandshakeContext(ctx context.Context) {
	t.hCtx = ctx
}

// handshake must be called after a non-nil handshake context
// has been set with setHandshakeContext().
func (t *tcpConn) handshake() error {
	if handshake := t.h; handshake != nil {
		var tCtxDone bool
		ctx, cancel := pkgcontext.WithCancelOnAnotherContext(t.hCtx, t.ctx, &tCtxDone)
		defer cancel()
		if err := handshake.do(ctx, t); err != nil {
			if pkgcontext.IsContextError(ctx, err) {
				if tCtxDone {
					return fmt.Errorf("(*tcpConn).ctx done while doing handshake: %w", err)
				}
				return fmt.Errorf("(*tcpConn).hCtx done while doing handshake: %w", err)
			}
			return err
		}
		t.h = nil
	}
	return nil
}

func (t *tcpConn) waitHandshake() error {
	if t.h == nil {
		return nil
	}

	select {
	case <-t.hCtx.Done():
		if t.h == nil {
			return nil
		}
		return fmt.Errorf("(*tcpConn).hCtx done while waiting for handshake: %w", t.hCtx.Err())
	case <-t.ctx.Done():
		return fmt.Errorf("(*tcpConn).ctx done while waiting for handshake: %w", t.ctx.Err())
	}
}

func (t *tcpConn) recv(segment gopacket.TransportLayer) {
	// forward to handshake first
	if handshake := t.h; handshake != nil {
		handshake.recv(segment)
		return
	}

	tcpSegment := segment.(*gplayers.TCP)

	// handle ACK
	if tcpSegment.ACK {
		select {
		case t.writeCh <- tcpSegment.Ack:
		default:
		}
	}

	// handle data
	if len(tcpSegment.Payload) > 0 {
		select {
		case t.readCh <- tcpSegment:
		default:
		}
	}

	// handle FIN
	if tcpSegment.FIN {
		t.Close()
	}
}

func (t *tcpConn) newDatagramHeaderAndSegment() (*gplayers.IPv4, *gplayers.TCP) {
	datagramHeader := &gplayers.IPv4{
		DstIP:    t.remoteAddr.ipAddress.Raw(),
		Protocol: gplayers.IPProtocolTCP,
	}
	if t.l.ipAddress != nil {
		datagramHeader.SrcIP = t.l.ipAddress.Raw()
	}
	segment := &gplayers.TCP{
		DstPort: gplayers.TCPPort(t.remoteAddr.port),
		SrcPort: gplayers.TCPPort(t.l.port),
		Window:  TCPWindowSize,
	}
	return datagramHeader, segment
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
	ctxDone := ctx.Done()
	if deadlineExceeded {
		return 0, ErrDeadlineExceeded
	}
	if ctx.Err() != nil {
		return 0, fmt.Errorf("(*tcpConn).ctx done before reading bytes: %w", ctx.Err())
	}

	// if some bytes are already available, return them right away
	if len(t.readBuf) > 0 {
		n := copy(b, t.readBuf)
		t.readBuf = t.readBuf[n:]
		if len(t.readBuf) == 0 {
			t.readBuf = nil
		}
		return n, nil
	}

	// no bytes available, block waiting for some
	for {
		// look up cache first
		var ok bool
		if t.readBuf, ok = t.readCache[t.ack]; ok {
			delete(t.readCache, t.ack)
		} else {
			// wait for an event if cache does not contain t.ack
			select {
			case <-ctxDone:
				if deadlineExceeded {
					return 0, ErrDeadlineExceeded
				}
				return 0, fmt.Errorf("(*tcpConn).ctx done while waiting for tcp segment: %w", ctx.Err())
			case segment := <-t.readCh:
				if seq := segment.Seq; seq != t.ack {
					// sequence number does not match the next expected byte (t.ack).
					// this might be a future segment, so just cache it for now.
					// make room in the cache if necessary
					if len(t.readCache) == tcpMaxReadCacheItems {
						for k := range t.readCache {
							delete(t.readCache, k)
							break
						}
					}
					t.readCache[seq] = segment.Payload
					continue
				}
				// sequence number matches the next expected byte. grab the payload
				t.readBuf = segment.Payload
			}
		}

		// FIXME(pimenta, #68)
		t.ack += uint32(len(t.readBuf))
		ackDatagramHeader, ackSegment := t.newDatagramHeaderAndSegment()
		ackSegment.ACK = true
		ackSegment.Ack = t.ack
		if err := t.l.s.transportLayer.send(ctx, ackDatagramHeader, ackSegment); err != nil {
			if deadlineExceeded {
				return 0, ErrDeadlineExceeded
			}
			return 0, fmt.Errorf("error sending tcp ack segment: %w", err)
		}

		n := copy(b, t.readBuf)
		t.readBuf = t.readBuf[n:]
		if len(t.readBuf) == 0 {
			t.readBuf = nil
		}
		return n, nil
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
	if deadlineExceeded {
		return 0, ErrDeadlineExceeded
	}
	if ctx.Err() != nil {
		return 0, fmt.Errorf("(*tcpConn).ctx done before writing bytes: %w", ctx.Err())
	}

	// send loop
	defer func() { t.seq += uint32(ackedBytes) }()
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
			segment.Seq = t.seq + uint32(nextByte)
			segment.Payload = b[nextByte:endOfPayload]
			if err = t.l.s.transportLayer.send(ctx, datagramHeader, segment); err != nil {
				if deadlineExceeded {
					err = ErrDeadlineExceeded
					return
				}
				err = fmt.Errorf("error sending tcp segment: %w", err)
				return
			}

			nextByte = endOfPayload
		}

		// wait for events
		err = func() error {
			timeout := time.NewTimer(tcpRetransmissionTimeout)
			defer timeout.Stop()
			select {
			case <-ctxDone:
				if deadlineExceeded {
					return ErrDeadlineExceeded
				}
				return fmt.Errorf("(*tcpConn).ctx done while waiting for tcp segment: %w", ctx.Err())
			case ack := <-t.writeCh:
				// handle ack number
				seq := int64(t.seq) + int64(ackedBytes)
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
				ackedBytes += int(delta)
				if int(nextByte) < ackedBytes {
					nextByte = int64(ackedBytes)
				}
			case <-timeout.C:
				// transmission timeout. reset window
				nextByte = int64(ackedBytes)
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
	// cancel ctx
	var cancel context.CancelFunc
	cancel, t.cancelCtx = t.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()

	// remove conn from listener so arriving segments are
	// not directed to this conn anymore
	t.l.deleteConn(t.remoteAddr)

	// close deadlines
	pkgio.Close(t.readDeadline, t.writeDeadline)

	// send FIN segment
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	finDatagramHeader, finSegment := t.newDatagramHeaderAndSegment()
	finSegment.FIN = true
	finSegment.Seq = t.seq
	if err := t.l.s.transportLayer.send(ctx, finDatagramHeader, finSegment); err != nil {
		return fmt.Errorf("error sending tcp fin segment: %w", err)
	}

	return nil
}

func (t *tcpConn) LocalAddr() net.Addr {
	return t.l.Addr()
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
