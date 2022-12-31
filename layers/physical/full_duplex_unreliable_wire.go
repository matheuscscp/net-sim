package physical

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/matheuscscp/net-sim/layers/common"
	"github.com/matheuscscp/net-sim/observability"
	pkgcontext "github.com/matheuscscp/net-sim/pkg/context"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/sirupsen/logrus"
)

type (
	// FullDuplexUnreliableWire represents a hypothetical guided
	// medium where you can send and receive bytes at the same time.
	// No guarantee is provided about the delivery/integrity.
	FullDuplexUnreliableWire interface {
		Send(ctx context.Context, payload []byte) (n int, err error)
		Recv(ctx context.Context, payload []byte) (n int, err error)
		Close() error
	}

	// FullDuplexUnreliableWireConfig contains the UDP configs for
	// the concrete implementation of FullDuplexUnreliableWire.
	FullDuplexUnreliableWireConfig struct {
		RecvUDPEndpoint string         `yaml:"recvUDPEndpoint"`
		SendUDPEndpoint string         `yaml:"sendUDPEndpoint"`
		Capture         *CaptureConfig `yaml:"capture"`
		MetricLabels    struct {
			StackName string `yaml:"stackName"`
		} `yaml:"metricLabels"`
	}

	// CaptureConfig allows specifying configurations for capturing
	// traffic in the pcapng format.
	CaptureConfig struct {
		Filename string `yaml:"filename"`
	}

	fullDuplexUnreliableWire struct {
		ctx            context.Context
		cancelCtx      context.CancelFunc
		conf           *FullDuplexUnreliableWireConfig
		conn           net.Conn
		wg             sync.WaitGroup
		captureCh      chan []byte
		recvdBytes     prometheus.Counter
		sentBytes      prometheus.Counter
		recvLatencyNs  prometheus.Observer
		sendLatencyNs  prometheus.Observer
		activeCaptures prometheus.Gauge
	}
)

const (
	promSubsystemFDUW        = "full_duplex_unreliable_wire"
	labelNameRecvUDPEndpoint = "recv_udp_endpoint"
	labelNameSendUDPEndpoint = "send_udp_endpoint"
)

var (
	metricLabelsFDUW = []string{
		observability.StackName,
		labelNameRecvUDPEndpoint,
		labelNameSendUDPEndpoint,
	}
	recvdBytes = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: promNamespace,
		Subsystem: promSubsystemFDUW,
		Name:      "recvd_bytes",
		Help:      "Total number of received bytes.",
	}, metricLabelsFDUW)
	sentBytes = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: promNamespace,
		Subsystem: promSubsystemFDUW,
		Name:      "sent_bytes",
		Help:      "Total number of sent bytes.",
	}, metricLabelsFDUW)
	recvLatencyNs = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Namespace: promNamespace,
		Subsystem: promSubsystemFDUW,
		Name:      "recv_latency_ns",
		Help:      "Latency in nanoseconds of FullDuplexUnreliableWire.Recv().",
	}, metricLabelsFDUW)
	sendLatencyNs = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Namespace: promNamespace,
		Subsystem: promSubsystemFDUW,
		Name:      "send_latency_ns",
		Help:      "Latency in nanoseconds of FullDuplexUnreliableWire.Send().",
	}, metricLabelsFDUW)
	activeCaptures = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: promNamespace,
		Subsystem: promSubsystemFDUW,
		Name:      "active_captures",
		Help:      "Number of go routines currently blocked on producing a wire capture into the capture channel.",
	}, metricLabelsFDUW)
)

// NewFullDuplexUnreliableWire creates a FullDuplexUnreliableWire from config.
func NewFullDuplexUnreliableWire(
	ctx context.Context,
	conf FullDuplexUnreliableWireConfig,
) (FullDuplexUnreliableWire, error) {
	// create UDP socket on the host network and wire
	recvAddr, err := net.ResolveUDPAddr("udp", conf.RecvUDPEndpoint)
	if err != nil {
		return nil, fmt.Errorf("error resolving udp address of recv endpoint: %w", err)
	}
	dialer := &net.Dialer{LocalAddr: recvAddr}
	conn, err := dialer.DialContext(ctx, "udp", conf.SendUDPEndpoint)
	if err != nil {
		return nil, fmt.Errorf("error dialing udp: %w", err)
	}
	wireCtx, cancel := context.WithCancel(context.Background())
	stackName := conf.MetricLabels.StackName
	if stackName == "" {
		stackName = "default"
	}
	metricLabels := prometheus.Labels{
		observability.StackName:  stackName,
		labelNameRecvUDPEndpoint: conf.RecvUDPEndpoint,
		labelNameSendUDPEndpoint: conf.SendUDPEndpoint,
	}
	f := &fullDuplexUnreliableWire{
		ctx:            wireCtx,
		cancelCtx:      cancel,
		conf:           &conf,
		conn:           conn,
		recvdBytes:     recvdBytes.With(metricLabels),
		sentBytes:      sentBytes.With(metricLabels),
		recvLatencyNs:  recvLatencyNs.With(metricLabels),
		sendLatencyNs:  sendLatencyNs.With(metricLabels),
		activeCaptures: activeCaptures.With(metricLabels),
	}

	if conf.Capture != nil {
		// open capture file and pcapng writer
		captureFile, err := os.Create(conf.Capture.Filename)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("error creating capture file %s: %w", conf.Capture.Filename, err)
		}
		captureWriter, err := pcapgo.NewNgWriter(captureFile, gplayers.LinkTypeEthernet)
		if err != nil {
			captureFile.Close()
			conn.Close()
			return nil, fmt.Errorf("error creating pcapng writer: %w", err)
		}

		// start capture thread
		f.captureCh = make(chan []byte, channelSize)
		f.wg.Add(1)
		go func() {
			defer func() {
				captureWriter.Flush()
				captureFile.Close()
				f.wg.Done()
			}()

			l := logrus.
				WithField("recv_udp_endpoint", conf.RecvUDPEndpoint).
				WithField("send_udp_endpoint", conf.SendUDPEndpoint)

			ctxDone := wireCtx.Done()
			for {
				select {
				case <-ctxDone:
					return
				case b := <-f.captureCh:
					err := captureWriter.WritePacket(gopacket.CaptureInfo{
						Timestamp:     time.Now(),
						CaptureLength: len(b),
						Length:        len(b),
					}, b)
					if err != nil {
						l.
							WithError(err).
							Error("error capturing data")
						continue
					}
					captureWriter.Flush()
				}
			}
		}()
	}

	return f, nil
}

func (f *fullDuplexUnreliableWire) Send(ctx context.Context, payload []byte) (n int, err error) {
	// validate payload size
	if len(payload) == 0 {
		return 0, common.ErrCannotSendEmpty
	}
	if len(payload) > MTU {
		return 0, fmt.Errorf("payload is larger than physical layer MTU (%d)", MTU)
	}

	c := net.Conn(f.conn)

	// initially, no timeout
	if err := c.SetWriteDeadline(time.Time{}); err != nil {
		return 0, fmt.Errorf("error setting write deadline to zero: %w", err)
	}

	// write in a separate thread
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		t0 := time.Now()
		n, err = c.Write(payload)
		f.sendLatencyNs.Observe(float64(time.Since(t0).Nanoseconds()))
		if err == nil {
			f.capture(payload[:n])
			f.sentBytes.Add(float64(n))
		}
	}()

	// wait for ch or for ctx.Done() and cancel the operation
	ctx, cancel := pkgcontext.WithCancelOnAnotherContext(ctx, f.ctx)
	defer cancel()
	select {
	case <-ctx.Done():
		if err := c.SetWriteDeadline(time.Now()); err != nil { // force timeout for ongoing blocked write
			return 0, fmt.Errorf("error forcing timeout after context done: %w", err)
		}
		<-ch
		return 0, ctx.Err()
	case <-ch:
		return
	}
}

func (f *fullDuplexUnreliableWire) Recv(ctx context.Context, payload []byte) (n int, err error) {
	c := net.Conn(f.conn)

	// initially, no timeout
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		return 0, fmt.Errorf("error setting read deadline to zero: %w", err)
	}

	// read in a separate thread
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		t0 := time.Now()
		n, err = c.Read(payload)
		f.recvLatencyNs.Observe(float64(time.Since(t0).Nanoseconds()))
		if err == nil {
			f.capture(payload[:n])
			f.recvdBytes.Add(float64(n))
		} else if errors.Is(err, syscall.ECONNREFUSED) {
			n, err = 0, nil
		}
	}()

	// wait for ch or for ctx.Done() and cancel the operation
	ctx, cancel := pkgcontext.WithCancelOnAnotherContext(ctx, f.ctx)
	defer cancel()
	select {
	case <-ctx.Done():
		if err := c.SetReadDeadline(time.Now()); err != nil { // force timeout for ongoing blocked read
			return 0, fmt.Errorf("error forcing timeout after context done: %w", err)
		}
		<-ch
		return 0, ctx.Err()
	case <-ch:
		return
	}
}

func (f *fullDuplexUnreliableWire) Close() error {
	// cancel ctx
	var cancel context.CancelFunc
	cancel, f.cancelCtx = f.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()

	// wait threads
	f.wg.Wait()

	return f.conn.Close()
}

func (f *fullDuplexUnreliableWire) capture(b []byte) {
	if f.captureCh == nil {
		return
	}

	go func() {
		f.activeCaptures.Inc()
		defer f.activeCaptures.Dec()
		select {
		case f.captureCh <- b:
		case <-f.ctx.Done():
		}
	}()
}
