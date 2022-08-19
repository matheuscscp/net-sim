package common

type (
	// OperStatus are the possible states of a physical medium,
	// network interface card, network interface, etc.
	OperStatus int
)

const (
	OperStatusDown OperStatus = iota
	OperStatusUp
)
