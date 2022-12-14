package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/matheuscscp/net-sim/hostnetwork"
	"github.com/matheuscscp/net-sim/layers/application"
	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/transport"
	pkgio "github.com/matheuscscp/net-sim/pkg/io"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	httpProxyCmd = &cobra.Command{
		Use:   "http-proxy <overlay-network-yaml-config-file> <<src-addr> <host-header> <dst-addr>>...",
		Short: "Proxy HTTP requests between the host network and the overlay network",
		Long: `Proxy HTTP requests from a src (addr, host) pair to a dst addr, where
the address syntax is the same of the tcp-proxy command except that
ports can be omitted (and default to 80).

An HTTP server will be created on each src address and each request
will be proxied to the dst address mapped by the Host header, or
dropped with 404 if the Host header is not mapped.

The same port uniqueness rule of the tcp-proxy command applies,
except if all the multiple occurrences of a given src port within
the same network (host or overlay) have the exact same <ipv4> part
on the src address and pairwise-distinct Host headers.`,
		Example: `  # will proxy requests with Host header google.com on port 80 on the
  # overlay network to addr 127.0.0.1:4321 on the host network
  net-sim http-proxy overlay-network.yml '' google.com host::4321`,
		Args: cobra.MinimumNArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			return httpProxy(args)
		},
	}
)

func init() {
	rootCmd.AddCommand(httpProxyCmd)
}

func httpProxy(args []string) error {
	hostTransport := hostnetwork.NewTransportLayer()
	hostRoundTripper := http.DefaultTransport
	ctx, cancel := newProcessContext()
	defer cancel()

	// create overlay network, transport and round tripper from config
	overlayNetworkConfFile := args[0]
	overlayNetwork, err := network.NewLayerFromConfigFile(ctx, overlayNetworkConfFile)
	if err != nil {
		return err
	}
	overlayTransport := transport.NewLayer(overlayNetwork)
	overlayRoundTripper := application.NewHTTPRoundTripper(overlayTransport)

	// create host and overlay networks api maps
	transportMap := map[string]transport.Layer{
		host:    hostTransport,
		overlay: overlayTransport,
	}
	roundTripperMap := map[string]http.RoundTripper{
		host:    hostRoundTripper,
		overlay: overlayRoundTripper,
	}

	// create listeners
	httpProxyEntries, err := httpProxyEntries(args[1:]).entries()
	if err != nil {
		return err
	}
	srcToHostToProxy := make(map[addr]map[string]*httputil.ReverseProxy)
	for _, entry := range httpProxyEntries {
		hostToProxy, ok := srcToHostToProxy[entry.src]
		if !ok {
			hostToProxy = make(map[string]*httputil.ReverseProxy)
			srcToHostToProxy[entry.src] = hostToProxy
		}
		if _, ok := hostToProxy[entry.host]; ok {
			return fmt.Errorf("(src-addr=%s, host-header=%s) pair is repeated", entry.src, entry.host)
		}
		proxy := httputil.NewSingleHostReverseProxy(&url.URL{
			Scheme: "http",
			Host:   entry.dst.hostPort().orLoopback().orPort(80).String(),
		})
		proxy.Transport = roundTripperMap[entry.dst.networkOrDefault()]
		hostToProxy[entry.host] = proxy
	}
	srcToListener := make(map[addr]net.Listener)
	for src := range srcToHostToProxy {
		t := transportMap[src.networkOrDefault()]
		l, err := t.Listen(ctx, transport.TCP, src.hostPort().orPort(80).String())
		if err != nil {
			for _, lis := range srcToListener {
				lis.Close()
			}
			return fmt.Errorf("error listening on address '%s': %w", src, err)
		}
		srcToListener[src] = l
	}

	// start server threads
	srcToServer := make(map[addr]*http.Server)
	var wg sync.WaitGroup
	for src, lis := range srcToListener {
		src := src
		hostToProxy := srcToHostToProxy[src]
		l := logrus.
			WithField("src_addr", src)
		s := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				proxy, ok := hostToProxy[r.Host]
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					resp := []byte(fmt.Sprintf("host '%s' not found for src addr '%s'", r.Host, src))
					if _, err := w.Write(resp); err != nil {
						l.
							WithError(err).
							Error("error writing response")
					}
					return
				}
				w.Header().Set("Connection", "close")
				proxy.ServeHTTP(w, r)
			}),
		}
		srcToServer[src] = s

		lis := lis
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
				l.
					WithError(err).
					Error("error Serve()ing server")
			}
		}()
	}

	// wait for ctx and close
	<-ctx.Done()
	for src, s := range srcToServer {
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			logrus.
				WithError(err).
				WithField("src_addr", src).
				Error("error shutting down http server")
		}
	}
	wg.Wait()
	return pkgio.Close(overlayTransport, overlayNetwork, hostTransport)
}
