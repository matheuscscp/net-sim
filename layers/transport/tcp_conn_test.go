package transport_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/matheuscscp/net-sim/layers/application"
	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/transport"
	"github.com/matheuscscp/net-sim/test"

	petname "github.com/dustinkirkland/golang-petname"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
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

	// start http server
	serverListener, err := transportLayer.Listen(ctx, transport.TCP, ":80")
	require.NoError(t, err)
	require.NotNil(t, serverListener)
	server = &http.Server{
		Handler: h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(time.Duration(rand.Intn(5)) * 10 * time.Millisecond)
			msg := fmt.Sprintf("working: %s", r.URL.Query().Get("q"))
			n, err := w.Write([]byte(msg))
			assert.Equal(t, len(msg), n)
			assert.NoError(t, err)
		}), &http2.Server{}),
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := server.Serve(serverListener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("error Serve()ing http server: %v", err)
		}
	}()

	// make multiple http1 requests in parallel, each one with a new client (hence new connection)
	var wgReqs sync.WaitGroup
	defer wgReqs.Wait()
	for i := 0; i < 10; i++ {
		wgReqs.Add(1)
		go func() {
			defer wgReqs.Done()
			client := &http.Client{Transport: application.NewHTTPRoundTripper(transportLayer)}
			pet := petname.Generate(2, "_")
			url := fmt.Sprintf("http://127.0.0.1:80?q=%s", pet)
			resp, err := client.Get(url)
			require.NoError(t, err)
			require.NotNil(t, resp)
			b, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			msg := fmt.Sprintf("working: %s", pet)
			assert.Equal(t, []byte(msg), b)
		}()
	}

	// make multiple http1 requests in parallel with the same client (possibly the same connection)
	client := &http.Client{Transport: application.NewHTTPRoundTripper(transportLayer)}
	for i := 0; i < 10; i++ {
		wgReqs.Add(1)
		go func() {
			defer wgReqs.Done()
			pet := petname.Generate(2, "_")
			url := fmt.Sprintf("http://127.0.0.1:80?q=%s", pet)
			resp, err := client.Get(url)
			require.NoError(t, err)
			require.NotNil(t, resp)
			b, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			msg := fmt.Sprintf("working: %s", pet)
			assert.Equal(t, []byte(msg), b)
		}()
	}

	// make multiple http2 requests in parallel with the same client (possibly the same connection)
	client2 := &http.Client{Transport: application.NewH2CRoundTripper(transportLayer)}
	for i := 0; i < 10; i++ {
		wgReqs.Add(1)
		go func() {
			defer wgReqs.Done()
			pet := petname.Generate(2, "_")
			url := fmt.Sprintf("http://127.0.0.1:80?q=%s", pet)
			resp, err := client2.Get(url)
			require.NoError(t, err)
			require.NotNil(t, resp)
			b, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			msg := fmt.Sprintf("working: %s", pet)
			assert.Equal(t, []byte(msg), b)
		}()
	}
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
