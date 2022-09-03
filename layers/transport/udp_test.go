package transport_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/matheuscscp/net-sim/layers/transport"
	"github.com/matheuscscp/net-sim/test"

	gplayers "github.com/google/gopacket/layers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUDPClientServer(t *testing.T) {
	sentSegments := make(chan *gplayers.UDP, 1)
	recvdDatagrams := make(chan *gplayers.IPv4, 1)
	networkLayer := test.NewMockNetworkLayer(t, sentSegments, recvdDatagrams)
	transportLayer := transport.NewLayer(networkLayer)
	defer func() {
		assert.NoError(t, transportLayer.Close())
		assert.NoError(t, networkLayer.Close())
		close(recvdDatagrams)
		close(sentSegments)
	}()

	server, err := transportLayer.Listen(transport.UDP, ":123")
	require.NoError(t, err)
	require.NotNil(t, server)

	client, err := transportLayer.Dial(context.Background(), transport.UDP, "127.0.0.1:123")
	require.NoError(t, err)
	require.NotNil(t, client)

	// client -> server
	helloPayload := []byte("hello world")
	n, err := client.Write(helloPayload)
	require.NoError(t, err)
	assert.Equal(t, len(helloPayload), n)
	test.AssertUDPSegment(
		t,
		sentSegments,
		65535, // src
		123,   // dst
		helloPayload,
	)
	test.RecvUDPSegment(t, recvdDatagrams, &gplayers.UDP{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		SrcPort: 65535,
		DstPort: 123,
		// the server accepts any src/dst IP address
	}, nil /*srcIPAddress*/, nil /*dstIPAddress*/)
	serverConn, err := server.Accept()
	require.NoError(t, err)
	require.NotNil(t, serverConn)
	readBuf := make([]byte, len(helloPayload)*2)
	require.NoError(t, serverConn.SetReadDeadline(time.Now().Add(10*time.Millisecond)))
	n, err = serverConn.Read(readBuf)
	require.NoError(t, err)
	assert.Equal(t, len(helloPayload), n)

	// server -> client
	n, err = serverConn.Write(helloPayload)
	require.NoError(t, err)
	assert.Equal(t, len(helloPayload), n)
	test.AssertUDPSegment(
		t,
		sentSegments,
		123,   // src
		65535, // dst
		helloPayload,
	)
	test.RecvUDPSegment(t, recvdDatagrams, &gplayers.UDP{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		SrcPort: 123,
		DstPort: 65535,
		// the client only accepts the dialed IP address as src IP address
	}, net.ParseIP("127.0.0.1") /*srcIPAddress*/, nil /*dstIPAddress*/)
	require.NoError(t, client.SetReadDeadline(time.Now().Add(10*time.Millisecond)))
	n, err = client.Read(readBuf)
	require.NoError(t, err)
	assert.Equal(t, len(helloPayload), n)

	// wrong src IP address on server -> client will be dropped, so we test
	// with a timeout
	test.RecvUDPSegment(t, recvdDatagrams, &gplayers.UDP{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		SrcPort: 123,
		DstPort: 65535,
		// the client only accepts the dialed IP address as src IP address
	}, net.ParseIP("99.0.0.1") /*srcIPAddress*/, nil /*dstIPAddress*/)
	require.NoError(t, client.SetReadDeadline(time.Now().Add(10*time.Millisecond)))
	n, err = client.Read(readBuf)
	assert.Error(t, err)
	assert.Equal(t, transport.ErrTimeout, err)
	assert.Equal(t, 0, n)

	for _, c := range []io.Closer{serverConn, client, server} {
		assert.NoError(t, c.Close())
	}
}

func TestUDPLocalServer(t *testing.T) {
	sentSegments := make(chan *gplayers.UDP, 1)
	recvdDatagrams := make(chan *gplayers.IPv4, 1)
	networkLayer := test.NewMockNetworkLayer(t, sentSegments, recvdDatagrams)
	transportLayer := transport.NewLayer(networkLayer)
	defer func() {
		assert.NoError(t, transportLayer.Close())
		assert.NoError(t, networkLayer.Close())
		close(recvdDatagrams)
		close(sentSegments)
	}()

	// specifying the loopback IP address makes the server "local"
	server, err := transportLayer.Listen(transport.UDP, "127.0.0.1:123")
	require.NoError(t, err)
	require.NotNil(t, server)

	client, err := transportLayer.Dial(context.Background(), transport.UDP, "127.0.0.1:123")
	require.NoError(t, err)
	require.NotNil(t, client)

	// wrong dst IP address on client -> server will be dropped, so we test
	// it before a valid packet, proving that it was discarded because only
	// the valid packet generated a pending connection
	helloWrongIP := []byte("hello wrong IP")
	test.RecvUDPSegment(t, recvdDatagrams, &gplayers.UDP{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloWrongIP,
		},
		SrcPort: 65535,
		DstPort: 123,
		// the server only accepts loopback as the dst IP address
	}, nil /*srcIPAddress*/, net.ParseIP("99.0.0.1") /*dstIPAddress*/)

	// we also take the opportunity to prove that a segment with correct
	// dst IP address but wrong dst port is also discarded, and let the
	// Accept() call below fetch the only valid connection from a client
	// to the "local" server
	helloWrongPort := []byte("hello wrong port")
	test.RecvUDPSegment(t, recvdDatagrams, &gplayers.UDP{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloWrongPort,
		},
		SrcPort: 65535,
		DstPort: 321,
		// the server only accepts loopback as the dst IP address
	}, nil /*srcIPAddress*/, net.ParseIP("127.0.0.1") /*dstIPAddress*/)

	// correct dst port and IP address
	helloPayload := []byte("hello world")
	test.RecvUDPSegment(t, recvdDatagrams, &gplayers.UDP{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		SrcPort: 65535,
		DstPort: 123,
		// the server only accepts loopback as the dst IP address
	}, nil /*srcIPAddress*/, net.ParseIP("127.0.0.1") /*dstIPAddress*/)
	serverConn, err := server.Accept()
	require.NoError(t, err)
	require.NotNil(t, serverConn)
	readBuf := make([]byte, len(helloPayload)*2)
	require.NoError(t, serverConn.SetReadDeadline(time.Now().Add(10*time.Millisecond)))
	n, err := serverConn.Read(readBuf)
	require.NoError(t, err)
	assert.Equal(t, len(helloPayload), n)

	// and finally we also take the opportunity to show that if
	// the serverConn and the server were Close()d, then valid
	// new connections are dropped
	require.NoError(t, serverConn.Close())
	require.NoError(t, server.Close())
	test.RecvUDPSegment(t, recvdDatagrams, &gplayers.UDP{
		BaseLayer: gplayers.BaseLayer{
			Payload: helloPayload,
		},
		SrcPort: 65535,
		DstPort: 123,
		// the server only accepts loopback as the dst IP address
	}, nil /*srcIPAddress*/, net.ParseIP("127.0.0.1") /*dstIPAddress*/)
	serverConn, err = server.Accept()
	assert.Error(t, err)
	assert.Equal(t, transport.ErrListenerClosed, err)
	assert.Nil(t, serverConn)

	for _, c := range []io.Closer{client, server} {
		assert.NoError(t, c.Close())
	}
}
