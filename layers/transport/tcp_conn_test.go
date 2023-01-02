package transport_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/matheuscscp/net-sim/layers/application"
	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/transport"
	"github.com/matheuscscp/net-sim/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTCPConn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var wg sync.WaitGroup
	var networkLayer network.Layer
	var transportLayer transport.Layer
	var server *http.Server

	defer func() {
		cancel()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, server.Shutdown(shutdownCtx))
		wg.Wait()
		assert.NoError(t, transportLayer.Close())
		assert.NoError(t, networkLayer.Close())
		for _, intf := range networkLayer.Interfaces() {
			assert.NoError(t, intf.Close())
			test.CloseEthPortsAndFlagErrorForUnexpectedData(t, intf.Card())
		}
	}()

	// start network
	networkLayer, err := network.NewLayer(ctx, network.LayerConfig{
		DefaultRouteInterface: "lo",
	})
	require.NoError(t, err)
	require.NotNil(t, networkLayer)
	transportLayer = transport.NewLayer(networkLayer)
	httpRoundTripper := application.NewHTTPRoundTripper(transportLayer)

	// start server
	serverListener, err := transportLayer.Listen(ctx, transport.TCP, ":80")
	require.NoError(t, err)
	require.NotNil(t, serverListener)
	server = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			msg := "working"
			n, err := w.Write([]byte(msg))
			assert.Equal(t, len(msg), n)
			assert.NoError(t, err)
		}),
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := server.Serve(serverListener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("error Serve()ing http server: %v", err)
		}
	}()

	// create client
	client := &http.Client{
		Transport: httpRoundTripper,
	}

	// make request
	resp, err := client.Get("http://127.0.0.1:80/")
	require.NoError(t, err)
	require.NotNil(t, resp)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, []byte("working"), b)
}

func TestTCPServerNotListening(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var networkLayer network.Layer
	var transportLayer transport.Layer

	defer func() {
		cancel()
		assert.NoError(t, transportLayer.Close())
		assert.NoError(t, networkLayer.Close())
		test.CloseIntfsAndFlagErrorForUnexpectedData(t, networkLayer.Interfaces()...)
	}()

	// start network
	networkLayer, err := network.NewLayer(ctx, network.LayerConfig{
		DefaultRouteInterface: "lo",
	})
	require.NoError(t, err)
	require.NotNil(t, networkLayer)
	transportLayer = transport.NewLayer(networkLayer)

	// dial
	c, err := transportLayer.Dial(ctx, transport.TCP, "127.0.0.1:80")
	assert.Nil(t, c)
	require.NotNil(t, err)
	assert.Contains(t, err.Error(), "connection reset")
}
