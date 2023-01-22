package transport

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/gopacket"
)

type (
	listenerNotFoundError struct {
		segment gopacket.TransportLayer
		addr    string
	}

	connNotFoundError struct {
		segment    gopacket.TransportLayer
		localAddr  string
		remoteAddr string
	}
)

const (
	useOfClosedConn = "use of closed network connection"
)

var (
	ErrInvalidNetwork       = errors.New("invalid network")
	ErrPortAlreadyInUse     = errors.New("port already in use")
	ErrAllPortsAlreadyInUse = errors.New("all ports already in use")
	ErrProtocolClosed       = errors.New("protocol closed")
	ErrListenerClosed       = fmt.Errorf("listener closed (os error msg: %s)", useOfClosedConn)
	ErrDeadlineExceeded     = errors.New("deadline exceeded")
	ErrConnClosed           = errors.New("connection closed")
	ErrConnReset            = errors.New("connection reset")

	errResetDelay = errors.New("reset delay")
)

func (l *listenerNotFoundError) Error() string {
	return fmt.Sprintf("listener not found for dst address %s", l.addr)
}

func (c *connNotFoundError) Error() string {
	return fmt.Sprintf("conn from %s not found for listener at %s", c.remoteAddr, c.localAddr)
}

// IsUseOfClosedConn tells whether the error is due to the port/connection
// being closed.
func IsUseOfClosedConn(err error) bool {
	return strings.Contains(err.Error(), useOfClosedConn)
}
