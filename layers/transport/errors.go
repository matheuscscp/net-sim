package transport

import (
	"errors"
	"fmt"
	"strings"
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
)

// IsUseOfClosedConn tells whether the error is due to the port/connection
// being closed.
func IsUseOfClosedConn(err error) bool {
	return strings.Contains(err.Error(), useOfClosedConn)
}
