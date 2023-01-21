package application

import (
	"net/http"
	"time"

	"github.com/matheuscscp/net-sim/layers/transport"
)

// NewHTTPRoundTripper returns an http.RoundTripper for making HTTP requests.
func NewHTTPRoundTripper(transportLayer transport.Layer) *http.Transport {
	return &http.Transport{
		DialContext:           transportLayer.Dial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}
