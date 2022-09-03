package physical

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/matheuscscp/net-sim/layers/common"
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
		RecvUDPEndpoint string `yaml:"recvUDPEndpoint"`
		SendUDPEndpoint string `yaml:"sendUDPEndpoint"`
	}

	fullDuplexUnreliableWire struct {
		conf *FullDuplexUnreliableWireConfig
		conn *net.UDPConn
	}
)

// NewFullDuplexUnreliableWire creates a FullDuplexUnreliableWire from config.
func NewFullDuplexUnreliableWire(
	ctx context.Context,
	conf FullDuplexUnreliableWireConfig,
) (FullDuplexUnreliableWire, error) {
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
	return &fullDuplexUnreliableWire{
		conf: &conf,
		conn: udpConn,
	}, nil
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
	if c := f.conn; c != nil {
		f.conn = nil
		return c.Close()
	}
	return nil
}
