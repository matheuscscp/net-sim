package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/matheuscscp/net-sim/layers/application"
	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/transport"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	httpProxyCmd = &cobra.Command{
		Use:   "http-proxy",
		Short: "Proxy HTTP requests between the host network and the overlay network",
	}

	httpProxyReverseCmd = &cobra.Command{
		Use:   "reverse <overlay-network-yaml-config-file> <<host-addr> <server-hostname> <overlay-addr>>...",
		Short: "Proxy HTTP requests from the host network to the overlay network",
		Args:  cobra.MinimumNArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			return httpProxy(args, true /*reverse*/)
		},
	}

	httpProxyForwardCmd = &cobra.Command{
		Use:   "forward <overlay-network-yaml-config-file> <<overlay-addr> <server-hostname> <host-addr>>...",
		Short: "Proxy HTTP requests from the overlay network to the host network",
		Args:  cobra.MinimumNArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			return httpProxy(args, false /*reverse*/)
		},
	}
)

func init() {
	httpProxyCmd.AddCommand(httpProxyReverseCmd)
	httpProxyCmd.AddCommand(httpProxyForwardCmd)
	rootCmd.AddCommand(httpProxyCmd)
}

func httpProxy(args []string, reverse bool) error {
	hostTransport := transport.NewHostLayer()
	hostRoundTripper := http.DefaultTransport
	ctx, cancel := contextWithCancelOnInterrupt(context.Background())
	defer cancel()

	// read overlay network config and create
	overlayNetworkConfFile := args[0]
	b, err := os.ReadFile(overlayNetworkConfFile)
	if err != nil {
		return fmt.Errorf("error reading yaml overlay network config file: %w", err)
	}
	var overlayNetworkConf network.LayerConfig
	if err := yaml.Unmarshal(b, &overlayNetworkConf); err != nil {
		return fmt.Errorf("error decoding overlay network config from yaml: %w", err)
	}
	overlayNetwork, err := network.NewLayer(ctx, overlayNetworkConf)
	if err != nil {
		return err
	}
	overlayTransport := transport.NewLayer(overlayNetwork)
	overlayRoundTripper := application.NewHTTPRoundTripper(overlayTransport)

	// swap networks for forward proxy mode
	hostLabel, overlayLabel := "host", "overlay"
	hostAddrLabel, overlayAddrLabel := fmt.Sprintf("%s_addr", hostLabel), fmt.Sprintf("%s_addr", overlayLabel)
	if !reverse { // forward
		hostTransport, overlayTransport = overlayTransport, hostTransport

		// hostRoundTripper, overlayRoundTripper = overlayRoundTripper, hostRoundTripper // linter complains
		overlayRoundTripper = hostRoundTripper

		// hostAddrLabel, overlayAddrLabel = overlayAddrLabel, hostAddrLabel // linter complains
		hostAddrLabel = overlayAddrLabel

		hostLabel, overlayLabel = overlayLabel, hostLabel
	}

	// create host listeners (bind host addrs)
	addrs := args[1:]
	if len(addrs)%3 != 0 {
		return errors.New("number of specified server arguments is not multiple of three")
	}
	hostAddrToServerHostnameToOverlayAddr := make(map[string]map[string]string)
	for i := 0; i < len(addrs); i += 3 {
		hostAddr, serverHostname, overlayAddr := addrs[i], addrs[i+1], addrs[i+2]
		serverHostnameToOverlayAddr, ok := hostAddrToServerHostnameToOverlayAddr[hostAddr]
		if !ok {
			serverHostnameToOverlayAddr = make(map[string]string)
			hostAddrToServerHostnameToOverlayAddr[hostAddr] = serverHostnameToOverlayAddr
		}
		serverHostnameToOverlayAddr[serverHostname] = overlayAddr
	}
	hostAddrToListener := make(map[string]net.Listener)
	for hostAddr := range hostAddrToServerHostnameToOverlayAddr {
		l, err := hostTransport.Listen(ctx, transport.TCP, hostAddr)
		if err != nil {
			for _, lis := range hostAddrToListener {
				lis.Close()
			}
			return fmt.Errorf("error listening on %s address '%s': %w", hostLabel, hostAddr, err)
		}
		hostAddrToListener[hostAddr] = l
	}

	// start server threads
	hostAddrToServer := make(map[string]*http.Server)
	var wg sync.WaitGroup
	for hostAddr, lis := range hostAddrToListener {
		l := logrus.
			WithField(hostAddrLabel, hostAddr)
		serverHostnameToOverlayAddr := hostAddrToServerHostnameToOverlayAddr[hostAddr]
		s := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				serverHostname := r.Header.Get("Host")
				overlayAddr, ok := serverHostnameToOverlayAddr[serverHostname]
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					resp := []byte(fmt.Sprintf("server hostname '%s' not found", serverHostname))
					if n, err := w.Write(resp); err != nil {
						l.
							WithError(err).
							WithField("bytes_written", n).
							Error("error writing response")
					}
					return
				}
				rawOverlayURL := fmt.Sprintf("http://%s", overlayAddr)
				overlayURL, err := url.Parse(rawOverlayURL)
				if err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					resp := []byte(fmt.Sprintf("error parsing %s URL '%s': %v", overlayLabel, rawOverlayURL, err))
					if n, err := w.Write(resp); err != nil {
						l.
							WithError(err).
							WithField("bytes_written", n).
							Error("error writing response")
					}
					return
				}
				proxy := httputil.NewSingleHostReverseProxy(overlayURL)
				proxy.Transport = overlayRoundTripper
				proxy.ServeHTTP(w, r)
			}),
		}
		hostAddrToServer[hostAddr] = s

		lis := lis
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
				l.
					WithError(err).
					Errorf("error Serve()ing %s server", hostLabel)
			}
		}()
	}

	// wait for ctx and close
	<-ctx.Done()
	logrus.Info("interruption signal received, closing servers...")
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for hostAddr, s := range hostAddrToServer {
		if err := s.Shutdown(ctx); err != nil {
			logrus.
				WithError(err).
				WithField(hostAddrLabel, hostAddr).
				Errorf("error shutting down %s server", hostLabel)
		}
	}
	wg.Wait()
	hostTransport.Close()
	overlayTransport.Close()
	overlayNetwork.Close()

	// drain remaining data
	for _, intf := range overlayNetwork.Interfaces() {
		for range intf.Recv() {
		}
		if intf.Card() != nil { // loopback
			for range intf.Card().Recv() {
			}
		}
	}

	return nil
}
