package application

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	"github.com/matheuscscp/net-sim/layers/transport"

	"golang.org/x/net/http2"
)

// NewHTTP2RoundTripper returns an http.RoundTripper for making HTTP2 requests.
func NewHTTP2RoundTripper(transportLayer transport.Layer) *http2.Transport {
	return &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, remoteAddr string, tlsConf *tls.Config) (net.Conn, error) {
			conn, err := transportLayer.Dial(ctx, network, remoteAddr)
			if err != nil {
				return nil, fmt.Errorf("error dialing transport: %w", err)
			}
			return tls.Client(conn, tlsConf), nil
		},
	}
}
