package transport_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matheuscscp/net-sim/layers/application"
	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/transport"
	"github.com/matheuscscp/net-sim/test"

	petname "github.com/dustinkirkland/golang-petname"
	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type (
	instrumentedTransportLayer struct {
		transport.Layer
		dialCnt int
		dialMu  sync.Mutex
	}
)

func (i *instrumentedTransportLayer) Dial(ctx context.Context, network, remoteAddr string) (net.Conn, error) {
	i.dialMu.Lock()
	i.dialCnt++
	i.dialMu.Unlock()
	return i.Layer.Dial(ctx, network, remoteAddr)
}

func TestTCPConn(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	var wg sync.WaitGroup
	var networkLayer network.Layer
	var transportLayer *instrumentedTransportLayer
	var server *http.Server

	defer func() {
		cancel()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		assert.NoError(t, server.Shutdown(shutdownCtx))
		wg.Wait()
		assert.NoError(t, transportLayer.Close())
		assert.NoError(t, networkLayer.Close())
	}()

	// start network
	networkLayer, err := network.NewLayer(ctx, network.LayerConfig{
		DefaultRouteInterface: "lo",
	})
	require.NoError(t, err)
	require.NotNil(t, networkLayer)
	transportLayer = &instrumentedTransportLayer{Layer: transport.NewLayer(networkLayer)}
	tcp, ok := networkLayer.GetRegisteredIPProtocol(gplayers.IPProtocolTCP)
	require.True(t, ok)
	require.NotNil(t, tcp)
	networkLayer.RegisterIPProtocol(&test.MockIPProtocol{
		IPProtocol: tcp.GetID(),
		RecvFunc: func(datagram *gplayers.IPv4) {
			switch rand.Intn(10) {
			// drop 10% of the datagrams
			case 0:
				return
			// poison ack number of 10% of the ACK segments (or drop
			// those 10% of datagrams which are not ACK segments)
			case 1:
				// decap
				segment, err := transport.DeserializeTCPSegment(datagram)
				require.NoError(t, err)
				require.NotNil(t, segment)
				if segment.SYN || !segment.ACK { // drop non-ACKs
					return
				}

				// poison ack
				segment.Ack = rand.Uint32()

				// re-encap datagram to ensure TCP checksum will be correct
				b, err := network.SerializeDatagramWithTransportSegment(datagram, segment)
				require.NoError(t, err)

				// decap datagram and deliver to TCP
				datagram, err := network.DeserializeDatagram(b)
				require.NoError(t, err)
				require.NotNil(t, datagram)
				tcp.Recv(datagram)
			// do not mess with the remaining 80% of the datagrams (just deliver to TCP)
			default:
				tcp.Recv(datagram)
			}
		},
	})

	// start h2c server
	serverListener, err := transportLayer.Listen(ctx, transport.TCP, ":80")
	require.NoError(t, err)
	require.NotNil(t, serverListener)
	server = &http.Server{
		Handler: h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			if len(b) == 0 {
				resp := []byte(http.StatusText(http.StatusBadRequest))
				w.WriteHeader(http.StatusBadRequest)
				n, err := w.Write(resp)
				assert.Equal(t, len(resp), n)
				assert.NoError(t, err)
				return
			}
			time.Sleep(time.Duration(rand.Intn(5)) * 10 * time.Millisecond)
			msg := fmt.Sprintf("working: %s", string(b))
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

	// helper function to make and test a single request to the h2c server
	makeReqWithData := func(client *http.Client, data string) {
		req, err := http.NewRequest("POST", "http://127.0.0.1:80", strings.NewReader(data))
		require.NoError(t, err)
		resp, err := client.Do(req.WithContext(ctx))
		require.NoError(t, err)
		require.NotNil(t, resp)
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		msg := fmt.Sprintf("working: %s", data)
		assert.Equal(t, []byte(msg), b)
	}
	makeReq := func(client *http.Client) {
		makeReqWithData(client, petname.Generate(2, "_"))
	}

	// make multiple http1 requests in parallel, each one with its own client, hence its own connection
	var wgReqs sync.WaitGroup
	defer func() {
		wgReqs.Wait()
		assert.Equal(t, 21, transportLayer.dialCnt)
	}()
	for i := 0; i < 10; i++ {
		wgReqs.Add(1)
		go func() {
			defer wgReqs.Done()
			client := &http.Client{Transport: application.NewHTTPRoundTripper(transportLayer)}
			makeReq(client)
		}()
	}

	// make multiple http1 requests in parallel with the same client, but not the same connection because of http1
	client := &http.Client{Transport: application.NewHTTPRoundTripper(transportLayer)}
	for i := 0; i < 10; i++ {
		wgReqs.Add(1)
		go func() {
			defer wgReqs.Done()
			makeReq(client)
		}()
	}

	// make multiple h2c requests in parallel with the same client, hence with the same connection
	client2 := &http.Client{Transport: application.NewH2CRoundTripper(transportLayer)}
	for i := 0; i < 10; i++ {
		wgReqs.Add(1)
		go func() {
			defer wgReqs.Done()
			makeReq(client2)
		}()
	}

	// make large request
	largeData := petname.Generate(2, "_")
	largeData += ", " + petname.Generate(2, "_")
	largeData += ", " + petname.Generate(2, "_")
	for len(largeData) < 1000000 { // 1 MB
		largeData += ", " + largeData
	}
	makeReqWithData(client2, largeData)
}

func TestTCPServerNotListening(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	var networkLayer network.Layer
	var transportLayer transport.Layer

	defer func() {
		cancel()
		assert.NoError(t, transportLayer.Close())
		assert.NoError(t, networkLayer.Close())
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

func TestTCPHandshakeWrongSeq(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	var networkLayer network.Layer
	var transportLayer transport.Layer
	var wg sync.WaitGroup

	defer func() {
		cancel()
		wg.Wait()
		assert.NoError(t, transportLayer.Close())
		assert.NoError(t, networkLayer.Close())
	}()

	// start network
	networkLayer, err := network.NewLayer(ctx, network.LayerConfig{
		DefaultRouteInterface: "lo",
	})
	require.NoError(t, err)
	require.NotNil(t, networkLayer)
	transportLayer = transport.NewLayer(networkLayer)
	tcp, ok := networkLayer.GetRegisteredIPProtocol(gplayers.IPProtocolTCP)
	require.True(t, ok)
	require.NotNil(t, tcp)
	poisoned := false
	networkLayer.RegisterIPProtocol(&test.MockIPProtocol{
		IPProtocol: tcp.GetID(),
		RecvFunc: func(datagram *gplayers.IPv4) {
			segment, err := transport.DeserializeTCPSegment(datagram)
			require.NoError(t, err)
			require.NotNil(t, segment)

			// poison the first SYN segment
			if !poisoned && segment.SYN && !segment.ACK {
				poisoned = true

				// poison seq
				segment.Seq = rand.Uint32()

				// re-encap datagram to ensure TCP checksum will be correct
				b, err := network.SerializeDatagramWithTransportSegment(datagram, segment)
				require.NoError(t, err)

				// decap datagram and deliver to TCP
				datagram, err := network.DeserializeDatagram(b)
				require.NoError(t, err)
				require.NotNil(t, datagram)
				tcp.Recv(datagram)

				return
			}

			tcp.Recv(datagram)
		},
	})

	// start server
	server, err := transportLayer.Listen(ctx, transport.TCP, ":80")
	require.NoError(t, err)
	require.NotNil(t, server)
	wg.Add(1)
	go func() {
		defer wg.Done()
		client, err := server.Accept()
		require.NoError(t, err)
		require.NotNil(t, client)
		n, err := client.Write([]byte("hello"))
		require.NotNil(t, err)
		assert.Contains(t, err.Error(), "connection reset")
		assert.Zero(t, n)
	}()

	// make request
	conn, err := transportLayer.Dial(ctx, transport.TCP, "127.0.0.1:80")
	require.NotNil(t, err)
	assert.Contains(t, err.Error(), "server sent wrong ack number")
	assert.Nil(t, conn)
}

func TestTCPRetrySYN(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	var networkLayer network.Layer
	var transportLayer transport.Layer
	var wg sync.WaitGroup

	defer func() {
		cancel()
		wg.Wait()
		assert.NoError(t, transportLayer.Close())
		assert.NoError(t, networkLayer.Close())
	}()

	// start network
	networkLayer, err := network.NewLayer(ctx, network.LayerConfig{
		DefaultRouteInterface: "lo",
	})
	require.NoError(t, err)
	require.NotNil(t, networkLayer)
	transportLayer = transport.NewLayer(networkLayer)
	tcp, ok := networkLayer.GetRegisteredIPProtocol(gplayers.IPProtocolTCP)
	require.True(t, ok)
	require.NotNil(t, tcp)
	dropped := false
	networkLayer.RegisterIPProtocol(&test.MockIPProtocol{
		IPProtocol: tcp.GetID(),
		RecvFunc: func(datagram *gplayers.IPv4) {
			segment, err := transport.DeserializeTCPSegment(datagram)
			require.NoError(t, err)
			require.NotNil(t, segment)

			// drop the first SYN segment
			if !dropped && segment.SYN && !segment.ACK {
				dropped = true
				return
			}

			tcp.Recv(datagram)
		},
	})

	// start server
	server, err := transportLayer.Listen(ctx, transport.TCP, ":80")
	require.NoError(t, err)
	require.NotNil(t, server)
	wg.Add(1)
	go func() {
		defer wg.Done()
		client, err := server.Accept()
		require.NoError(t, err)
		require.NotNil(t, client)
		n, err := client.Write([]byte("hello"))
		require.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.NoError(t, server.Close())
	}()

	// make request
	conn, err := transportLayer.Dial(ctx, transport.TCP, "127.0.0.1:80")
	require.NoError(t, err)
	require.NotNil(t, conn)
	buf := make([]byte, 100)
	n, err := conn.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "hello", string(buf[:n]))
	// assert.NoError(t, conn.Close()) // TODO(pimenta, #71): acknowledge FIN segments properly
}

func TestTCPRetrySYNACK(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	var networkLayer network.Layer
	var transportLayer transport.Layer
	var wg sync.WaitGroup

	defer func() {
		cancel()
		wg.Wait()
		assert.NoError(t, transportLayer.Close())
		assert.NoError(t, networkLayer.Close())
	}()

	// start network
	networkLayer, err := network.NewLayer(ctx, network.LayerConfig{
		DefaultRouteInterface: "lo",
	})
	require.NoError(t, err)
	require.NotNil(t, networkLayer)
	transportLayer = transport.NewLayer(networkLayer)
	tcp, ok := networkLayer.GetRegisteredIPProtocol(gplayers.IPProtocolTCP)
	require.True(t, ok)
	require.NotNil(t, tcp)
	dropped := false
	networkLayer.RegisterIPProtocol(&test.MockIPProtocol{
		IPProtocol: tcp.GetID(),
		RecvFunc: func(datagram *gplayers.IPv4) {
			segment, err := transport.DeserializeTCPSegment(datagram)
			require.NoError(t, err)
			require.NotNil(t, segment)

			// drop the first SYNACK segment
			if !dropped && segment.SYN && segment.ACK {
				dropped = true
				return
			}

			tcp.Recv(datagram)
		},
	})

	// start server
	server, err := transportLayer.Listen(ctx, transport.TCP, ":80")
	require.NoError(t, err)
	require.NotNil(t, server)
	wg.Add(1)
	go func() {
		defer wg.Done()
		client, err := server.Accept()
		require.NoError(t, err)
		require.NotNil(t, client)
		n, err := client.Write([]byte("hello"))
		require.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.NoError(t, server.Close())
	}()

	// make request
	conn, err := transportLayer.Dial(ctx, transport.TCP, "127.0.0.1:80")
	require.NoError(t, err)
	require.NotNil(t, conn)
	buf := make([]byte, 100)
	n, err := conn.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "hello", string(buf[:n]))
	// assert.NoError(t, conn.Close()) // TODO(pimenta, #71): acknowledge FIN segments properly
}
