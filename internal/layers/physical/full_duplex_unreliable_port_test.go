package physical_test

import (
	"context"
	"testing"

	"github.com/matheuscscp/net-sim/internal/layers/physical"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateConnectedPorts(t *testing.T) {
	port1, err := physical.NewFullDuplexUnreliablePort(context.Background(), &physical.FullDuplexUnreliablePortConfig{
		RecvUDPEndpoint: ":50001",
		SendUDPEndpoint: ":50002",
	})
	assert.NoError(t, err)
	assert.NotNil(t, port1)

	port2, err := physical.NewFullDuplexUnreliablePort(context.Background(), &physical.FullDuplexUnreliablePortConfig{
		RecvUDPEndpoint: ":50002",
		SendUDPEndpoint: ":50001",
	})
	assert.NoError(t, err)
	assert.NotNil(t, port2)

	msg1 := []byte("hello port2")
	n, err := port1.Send(context.Background(), msg1)
	assert.Equal(t, len(msg1), n)
	assert.NoError(t, err)

	msg2 := []byte("hello port1")
	n, err = port2.Send(context.Background(), msg2)
	assert.Equal(t, len(msg2), n)
	assert.NoError(t, err)

	buf := make([]byte, len(msg1))
	n, err = port2.Recv(context.Background(), buf)
	assert.Equal(t, len(msg1), n)
	assert.NoError(t, err)
	assert.Equal(t, msg1, buf)

	buf = make([]byte, len(msg2))
	n, err = port1.Recv(context.Background(), buf)
	assert.Equal(t, len(msg2), n)
	assert.NoError(t, err)
	assert.Equal(t, msg2, buf)

	assert.NoError(t, port2.Close())
	assert.NoError(t, port1.Close())
}

func TestTurnPortOffAndOn(t *testing.T) {
	port, err := physical.NewFullDuplexUnreliablePort(context.Background(), &physical.FullDuplexUnreliablePortConfig{
		RecvUDPEndpoint: ":50001",
		SendUDPEndpoint: ":50002",
	})
	require.NoError(t, err)
	require.NotNil(t, port)
	require.NoError(t, port.TurnOff())
	require.NoError(t, port.TurnOn(context.Background()))
	require.NoError(t, port.TurnOff())
	require.NoError(t, port.TurnOn(context.Background()))
	assert.NoError(t, port.Close())
}
