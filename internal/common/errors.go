package common

import "errors"

var (
	ErrCannotSendEmpty = errors.New("cannot send zero bytes")
)
