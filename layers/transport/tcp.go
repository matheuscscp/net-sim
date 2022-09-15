package transport

import (
	"context"
	"net"
	"time"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

type (
	tcp struct{}

	tcpConn struct {
		l          *listener
		remoteAddr addr
	}
)

func (*tcp) newConn(l *listener, remoteAddr addr) conn {
	return &tcpConn{
		l:          l,
		remoteAddr: remoteAddr,
	}
}

func (*tcp) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return nil, nil // TODO
}

func (t *tcpConn) handshakeDial(ctx context.Context) error {
	return nil // TODO
}

func (t *tcpConn) handshakeAccept(ctx context.Context) error {
	return nil // TODO
}

func (t *tcpConn) recv(segment gopacket.TransportLayer) {
	// TODO
}

func (t *tcpConn) Read(b []byte) (n int, err error) {
	return 0, nil // TODO
}

func (t *tcpConn) Write(b []byte) (n int, err error) {
	return 0, nil // TODO
}

func (t *tcpConn) Close() error {
	return nil // TODO
}

func (t *tcpConn) LocalAddr() net.Addr {
	return nil // TODO
}

func (t *tcpConn) RemoteAddr() net.Addr {
	return nil // TODO
}

func (t *tcpConn) SetDeadline(d time.Time) error {
	return nil // TODO
}

func (t *tcpConn) SetReadDeadline(d time.Time) error {
	return nil // TODO
}

func (t *tcpConn) SetWriteDeadline(d time.Time) error {
	return nil // TODO
}
