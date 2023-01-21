package application

import (
	"context"
	"crypto/tls"
	"net"

	"github.com/matheuscscp/net-sim/layers/transport"

	"golang.org/x/net/http2"
)

// NewH2CRoundTripper returns an http.RoundTripper for making H2C requests.
func NewH2CRoundTripper(transportLayer transport.Layer) *http2.Transport {
	return &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, remoteAddr string, _ *tls.Config) (net.Conn, error) {
			return transportLayer.Dial(ctx, network, remoteAddr)
		},
	}
}
