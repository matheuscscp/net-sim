package physical

import (
	"context"
	"errors"
	"fmt"
	"net"
)

type (
	// FullDuplexUnreliablePort represents a hypothetical port
	// where you can send and receive bytes at the same time.
	// Outbound bytes are not guaranteed to reach the other end.
	FullDuplexUnreliablePort interface {
		Send(ctx context.Context, payload []byte) (n int, err error)
		Recv(ctx context.Context, buf []byte) (n int, err error)
		TurnOn(ctx context.Context) error
		TurnOff() error
		Close() error
	}

	// FullDuplexUnreliablePortConfig contains the UDP configs for
	// the concrete implementation of FullDuplexUnreliablePort.
	FullDuplexUnreliablePortConfig struct {
		RecvUDPEndpoint string
		SendUDPEndpoint string
	}

	fullDuplexUnreliablePort struct {
		dialer *net.Dialer
		conf   *FullDuplexUnreliablePortConfig
		conn   *net.UDPConn
	}
)

// NewFullDuplexUnreliablePort creates a FullDuplexUnreliablePort from config.
func NewFullDuplexUnreliablePort(ctx context.Context, conf *FullDuplexUnreliablePortConfig) (FullDuplexUnreliablePort, error) {
	recvAddr, err := net.ResolveUDPAddr("udp", conf.RecvUDPEndpoint)
	if err != nil {
		return nil, fmt.Errorf("error resolving UDP address of recv endpoint: %w", err)
	}
	port := &fullDuplexUnreliablePort{
		dialer: &net.Dialer{
			LocalAddr: recvAddr,
		},
		conf: conf,
	}
	if err := port.TurnOn(ctx); err != nil {
		return nil, fmt.Errorf("error turning port on: %w", err)
	}
	return port, nil
}

func (f *fullDuplexUnreliablePort) Send(ctx context.Context, payload []byte) (n int, err error) {
	c := net.Conn(f.conn)
	if d, ok := ctx.Deadline(); ok {
		if err := c.SetWriteDeadline(d); err != nil {
			return 0, fmt.Errorf("error setting write deadline for udp socket: %w", err)
		}
	}
	return c.Write(payload)
}

func (f *fullDuplexUnreliablePort) Recv(ctx context.Context, buf []byte) (n int, err error) {
	c := net.Conn(f.conn)
	if d, ok := ctx.Deadline(); ok {
		if err := c.SetReadDeadline(d); err != nil {
			return 0, fmt.Errorf("error setting read deadline for udp socket: %w", err)
		}
	}
	return c.Read(buf)
}

func (f *fullDuplexUnreliablePort) TurnOn(ctx context.Context) error {
	if f.conn != nil {
		return errors.New("port is already on")
	}
	conn, err := f.dialer.DialContext(ctx, "udp", f.conf.SendUDPEndpoint)
	if err != nil {
		return fmt.Errorf("error dialing UDP: %w", err)
	}
	udpConn, ok := conn.(*net.UDPConn)
	if !ok {
		errMsg := "conn is not UDP, closing"
		if err := conn.Close(); err != nil {
			return fmt.Errorf("%s. error closing conn: %w", errMsg, err)
		}
		return errors.New(errMsg)
	}
	f.conn = udpConn
	return nil
}

func (f *fullDuplexUnreliablePort) TurnOff() error {
	if f == nil {
		return nil
	}
	if c := f.conn; c != nil {
		f.conn = nil
		return c.Close()
	}
	return nil
}

func (f *fullDuplexUnreliablePort) Close() error {
	return f.TurnOff()
}
