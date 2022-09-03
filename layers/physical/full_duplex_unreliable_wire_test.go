package physical_test

import (
	"context"
	"testing"

	"github.com/matheuscscp/net-sim/layers/physical"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectedWires(t *testing.T) {
	wire1, err := physical.NewFullDuplexUnreliableWire(context.Background(), physical.FullDuplexUnreliableWireConfig{
		RecvUDPEndpoint: ":50001",
		SendUDPEndpoint: ":50002",
	})
	require.NoError(t, err)
	require.NotNil(t, wire1)

	wire2, err := physical.NewFullDuplexUnreliableWire(context.Background(), physical.FullDuplexUnreliableWireConfig{
		RecvUDPEndpoint: ":50002",
		SendUDPEndpoint: ":50001",
	})
	require.NoError(t, err)
	require.NotNil(t, wire2)

	expected1 := []byte("hello wire2")
	n, err := wire1.Send(context.Background(), expected1)
	assert.Equal(t, len(expected1), n)
	assert.NoError(t, err)

	expected2 := []byte("hello wire1")
	n, err = wire2.Send(context.Background(), expected2)
	assert.Equal(t, len(expected2), n)
	assert.NoError(t, err)

	actual := make([]byte, len(expected1))
	n, err = wire2.Recv(context.Background(), actual)
	assert.Equal(t, len(expected1), n)
	assert.NoError(t, err)
	assert.Equal(t, expected1, actual)

	actual = make([]byte, len(expected2))
	n, err = wire1.Recv(context.Background(), actual)
	assert.Equal(t, len(expected2), n)
	assert.NoError(t, err)
	assert.Equal(t, expected2, actual)

	assert.NoError(t, wire1.Close())
	assert.NoError(t, wire2.Close())
}

func TestReadLessBytesThanBufSize(t *testing.T) {
	wire1, err := physical.NewFullDuplexUnreliableWire(context.Background(), physical.FullDuplexUnreliableWireConfig{
		RecvUDPEndpoint: ":50011",
		SendUDPEndpoint: ":50012",
	})
	require.NoError(t, err)
	require.NotNil(t, wire1)

	wire2, err := physical.NewFullDuplexUnreliableWire(context.Background(), physical.FullDuplexUnreliableWireConfig{
		RecvUDPEndpoint: ":50012",
		SendUDPEndpoint: ":50011",
	})
	require.NoError(t, err)
	require.NotNil(t, wire2)

	expected := []byte("hello wire2")
	n, err := wire1.Send(context.Background(), expected)
	assert.Equal(t, len(expected), n)
	assert.NoError(t, err)

	actual := make([]byte, len(expected)+5)
	n, err = wire2.Recv(context.Background(), actual)
	assert.Equal(t, len(expected), n)
	assert.NoError(t, err)
	assert.Equal(t, expected, actual[:n])

	assert.NoError(t, wire1.Close())
	assert.NoError(t, wire2.Close())
}
