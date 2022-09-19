package transport

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/matheuscscp/net-sim/layers/common"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/hashicorp/go-multierror"
)

type (
	udp struct{}

	udpConn struct {
		l             *listener
		remoteAddr    addr
		in            chan []byte
		readDeadline  *deadline
		writeDeadline *deadline
	}
)

func (udp) decap(datagram *gplayers.IPv4) (gopacket.TransportLayer, error) {
	return DeserializeUDPSegment(datagram)
}

func (udp) newClientHandshake() handshake {
	return nil // no-op
}

func (udp) newServerHandshake() handshake {
	return nil // no-op
}

func (udp) newConn(l *listener, remoteAddr addr, _ handshake) conn {
	return &udpConn{
		l:             l,
		remoteAddr:    remoteAddr,
		in:            make(chan []byte, channelSize),
		readDeadline:  newDeadline(),
		writeDeadline: newDeadline(),
	}
}

func (u *udpConn) setHandshakeContext(ctx context.Context) {
	// no-op
}

func (u *udpConn) handshake() error {
	return nil // no-op
}

func (u *udpConn) recv(segment gopacket.TransportLayer) {
	select {
	case u.in <- segment.LayerPayload():
	default:
	}
}

// Read blocks waiting for one UDP segment. b should have enough space for
// the whole UDP segment payload (any exceeding bytes will be discarded).
func (u *udpConn) Read(b []byte) (n int, err error) {
	ctx, cancel, deadlineExceeded := u.readDeadline.newContext()
	defer cancel()
	select {
	case payload := <-u.in:
		return copy(b, payload), nil
	case <-ctx.Done():
		if *deadlineExceeded {
			return 0, ErrDeadlineExceeded
		}
		return 0, ctx.Err()
	}
}

func (u *udpConn) Write(b []byte) (n int, err error) {
	// validate payload size
	if len(b) == 0 {
		return 0, common.ErrCannotSendEmpty
	}
	if len(b) > UDPMTU {
		return 0, fmt.Errorf("payload is larger than transport layer UDP MTU (%d)", UDPMTU)
	}

	// craft headers
	datagramHeader := &gplayers.IPv4{
		DstIP:    u.remoteAddr.ipAddress.Raw(),
		Protocol: gplayers.IPProtocolUDP,
	}
	if u.l.ipAddress != nil {
		datagramHeader.SrcIP = u.l.ipAddress.Raw()
	}
	segment := &gplayers.UDP{
		BaseLayer: gplayers.BaseLayer{
			Payload: b,
		},
		SrcPort: gplayers.UDPPort(u.l.port),
		DstPort: gplayers.UDPPort(u.remoteAddr.port),
		Length:  uint16(len(b) + UDPHeaderLength),
	}

	// send
	ctx, cancel, deadlineExceeded := u.writeDeadline.newContext()
	defer cancel()
	if u.writeDeadline.exceeded() {
		return 0, ErrDeadlineExceeded
	}
	if err := u.l.s.transportLayer.send(ctx, datagramHeader, segment); err != nil {
		if *deadlineExceeded {
			return 0, ErrDeadlineExceeded
		}
		return 0, fmt.Errorf("error sending udp segment: %w", err)
	}

	return len(b), nil
}

func (u *udpConn) Close() error {
	// remove conn from listener so arriving segments are
	// not directed to this conn anymore
	u.l.deleteConn(u.remoteAddr)

	u.readDeadline.close()
	u.writeDeadline.close()

	return nil
}

func (u *udpConn) LocalAddr() net.Addr {
	return u.l.Addr()
}

func (u *udpConn) RemoteAddr() net.Addr {
	return newUDPAddr(u.remoteAddr)
}

func (u *udpConn) SetDeadline(d time.Time) error {
	var err error
	if dErr := u.SetReadDeadline(d); dErr != nil {
		err = multierror.Append(err, fmt.Errorf("error setting read deadline: %w", err))
	}
	if dErr := u.SetWriteDeadline(d); dErr != nil {
		err = multierror.Append(err, fmt.Errorf("error setting write deadline: %w", err))
	}
	return err
}

func (u *udpConn) SetReadDeadline(d time.Time) error {
	u.readDeadline.set(d)
	return nil
}

func (u *udpConn) SetWriteDeadline(d time.Time) error {
	u.writeDeadline.set(d)
	return nil
}
