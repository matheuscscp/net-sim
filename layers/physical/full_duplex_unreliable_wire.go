package physical

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/matheuscscp/net-sim/layers/common"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
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
	}

	// CaptureConfig allows specifying configurations for capturing
	// traffic in the pcapng format.
	CaptureConfig struct {
		Filename string `yaml:"filename"`
	}

	fullDuplexUnreliableWire struct {
		conf      *FullDuplexUnreliableWireConfig
		conn      *net.UDPConn
		cancelCtx context.CancelFunc
		wg        sync.WaitGroup
		capture   chan []byte
	}
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
	udpConn, ok := conn.(*net.UDPConn)
	if !ok {
		errMsg := "conn is not udp, closing"
		if err := conn.Close(); err != nil {
			return nil, fmt.Errorf("%s. error closing conn: %w", errMsg, err)
		}
		return nil, errors.New(errMsg)
	}
	ctx, cancel := context.WithCancel(ctx)
	f := &fullDuplexUnreliableWire{
		conf:      &conf,
		conn:      udpConn,
		cancelCtx: cancel,
	}

	// start capture thread
	if conf.Capture != nil {
		captureFile, err := os.Create(conf.Capture.Filename)
		if err != nil {
			udpConn.Close()
			return nil, fmt.Errorf("error creating capture file %s: %w", conf.Capture.Filename, err)
		}
		captureWriter, err := pcapgo.NewNgWriter(captureFile, gplayers.LinkTypeEthernet)
		if err != nil {
			captureFile.Close()
			udpConn.Close()
			return nil, fmt.Errorf("error creating pcapng writer: %w", err)
		}
		f.capture = make(chan []byte, channelSize)
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

			ctxDone := ctx.Done()
			for {
				select {
				case <-ctxDone:
					return
				case cap := <-f.capture:
					err := captureWriter.WritePacket(gopacket.CaptureInfo{
						Timestamp:     time.Now(),
						CaptureLength: len(cap),
						Length:        len(cap),
					}, cap)
					if err != nil {
						l.
							WithError(err).
							Error("error capturing data")
					}
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
		n, err = c.Write(payload)
		if err == nil && f.capture != nil {
			f.capture <- payload[:n]
		}
	}()

	// wait for ctx cancel
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
		n, err = c.Read(payload)
		if err == nil && f.capture != nil {
			f.capture <- payload[:n]
		}
	}()

	// wait for ctx cancel
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
	// cancel ctx and wait threads
	var cancel context.CancelFunc
	cancel, f.cancelCtx = f.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()
	f.wg.Wait()

	// close channels
	if f.capture != nil {
		close(f.capture)
		for range f.capture {
		}
	}

	return f.conn.Close()
}
