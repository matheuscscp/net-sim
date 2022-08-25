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
	// FullDuplexUnreliablePort represents a hypothetical guided
	// medium where you can send and receive bytes at the same time.
	// No guarantee is provided about the delivery/integrity.
	FullDuplexUnreliablePort interface {
		Send(ctx context.Context, payload []byte) (n int, err error)
		Recv(ctx context.Context, payloadBuf []byte) (n int, err error)
		Close() error
	}

	// FullDuplexUnreliablePortConfig contains the UDP configs for
	// the concrete implementation of FullDuplexUnreliablePort.
	FullDuplexUnreliablePortConfig struct {
		RecvUDPEndpoint string `yaml:"recvUDPEndpoint"`
		SendUDPEndpoint string `yaml:"sendUDPEndpoint"`
	}

	fullDuplexUnreliablePort struct {
		conf *FullDuplexUnreliablePortConfig
		conn *net.UDPConn
	}
)

// NewFullDuplexUnreliablePort creates a FullDuplexUnreliablePort from config.
func NewFullDuplexUnreliablePort(
	ctx context.Context,
	conf FullDuplexUnreliablePortConfig,
) (FullDuplexUnreliablePort, error) {
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
	return &fullDuplexUnreliablePort{
		conf: &conf,
		conn: udpConn,
	}, nil
}

func (f *fullDuplexUnreliablePort) Send(ctx context.Context, payload []byte) (n int, err error) {
	if len(payload) == 0 {
		return 0, common.ErrCannotSendEmpty
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

func (f *fullDuplexUnreliablePort) Recv(ctx context.Context, payloadBuf []byte) (n int, err error) {
	c := net.Conn(f.conn)

	// initially, no timeout
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		return 0, fmt.Errorf("error setting read deadline to zero: %w", err)
	}

	// read in a separate thread
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		n, err = c.Read(payloadBuf)
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

func (f *fullDuplexUnreliablePort) Close() error {
	if c := f.conn; c != nil {
		f.conn = nil
		return c.Close()
	}
	return nil
}
