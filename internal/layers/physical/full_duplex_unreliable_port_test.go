package physical_test

import (
	"context"
	"testing"

	"github.com/matheuscscp/net-sim/internal/common"
	"github.com/matheuscscp/net-sim/internal/layers/physical"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateConnectedPorts(t *testing.T) {
	port1, err := physical.NewFullDuplexUnreliablePort(context.Background(), physical.FullDuplexUnreliablePortConfig{
		RecvUDPEndpoint: ":50001",
		SendUDPEndpoint: ":50002",
	})
	require.NoError(t, err)
	require.NotNil(t, port1)

	port2, err := physical.NewFullDuplexUnreliablePort(context.Background(), physical.FullDuplexUnreliablePortConfig{
		RecvUDPEndpoint: ":50002",
		SendUDPEndpoint: ":50001",
	})
	require.NoError(t, err)
	require.NotNil(t, port2)

	expected1 := []byte("hello port2")
	n, err := port1.Send(context.Background(), expected1)
	assert.Equal(t, len(expected1), n)
	assert.NoError(t, err)

	expected2 := []byte("hello port1")
	n, err = port2.Send(context.Background(), expected2)
	assert.Equal(t, len(expected2), n)
	assert.NoError(t, err)

	actual := make([]byte, len(expected1))
	n, err = port2.Recv(context.Background(), actual)
	assert.Equal(t, len(expected1), n)
	assert.NoError(t, err)
	assert.Equal(t, expected1, actual)

	actual = make([]byte, len(expected2))
	n, err = port1.Recv(context.Background(), actual)
	assert.Equal(t, len(expected2), n)
	assert.NoError(t, err)
	assert.Equal(t, expected2, actual)

	assert.NoError(t, port1.Close())
	assert.NoError(t, port2.Close())
}

func TestReadLessBytesThanBufSize(t *testing.T) {
	port1, err := physical.NewFullDuplexUnreliablePort(context.Background(), physical.FullDuplexUnreliablePortConfig{
		RecvUDPEndpoint: ":50001",
		SendUDPEndpoint: ":50002",
	})
	require.NoError(t, err)
	require.NotNil(t, port1)

	port2, err := physical.NewFullDuplexUnreliablePort(context.Background(), physical.FullDuplexUnreliablePortConfig{
		RecvUDPEndpoint: ":50002",
		SendUDPEndpoint: ":50001",
	})
	require.NoError(t, err)
	require.NotNil(t, port2)

	expected := []byte("hello port2")
	n, err := port1.Send(context.Background(), expected)
	assert.Equal(t, len(expected), n)
	assert.NoError(t, err)

	actual := make([]byte, len(expected)+5)
	n, err = port2.Recv(context.Background(), actual)
	assert.Equal(t, len(expected), n)
	assert.NoError(t, err)
	assert.Equal(t, expected, actual[:n])

	assert.NoError(t, port1.Close())
	assert.NoError(t, port2.Close())
}

func TestTurnPortOffAndOn(t *testing.T) {
	port, err := physical.NewFullDuplexUnreliablePort(context.Background(), physical.FullDuplexUnreliablePortConfig{
		RecvUDPEndpoint: ":50001",
		SendUDPEndpoint: ":50002",
	})
	require.NoError(t, err)
	require.NotNil(t, port)
	assert.Equal(t, common.OperStatusUp, port.OperStatus())
	require.NoError(t, port.TurnOff())
	assert.Equal(t, common.OperStatusDown, port.OperStatus())
	require.NoError(t, port.TurnOn(context.Background()))
	assert.Equal(t, common.OperStatusUp, port.OperStatus())
	require.NoError(t, port.TurnOff())
	assert.Equal(t, common.OperStatusDown, port.OperStatus())
	require.NoError(t, port.TurnOn(context.Background()))
	assert.Equal(t, common.OperStatusUp, port.OperStatus())
	assert.NoError(t, port.Close())
	assert.Equal(t, common.OperStatusDown, port.OperStatus())
}
