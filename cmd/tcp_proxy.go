package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/matheuscscp/net-sim/hostnetwork"
	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/transport"
	pkgio "github.com/matheuscscp/net-sim/pkg/io"

	"github.com/hashicorp/go-multierror"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	tcpProxyCmd = &cobra.Command{
		Use:   "tcp-proxy <overlay-network-yaml-config-file> <<src-addr> <dst-addr>>...",
		Short: "Proxy TCP connections between the host network and the overlay network",
		Long: `Proxy TCP connections between (src, dst) pairs of addresses of the form:

[<network>:][<ipv4>]:<port>

where <network> can be either overlay (the default) or host.

A listener will be created on the src address and each accepted
connection will be proxied to the dst address.

The <ipv4> part may be omitted, which defaults to accepting
connections to any dst IP address for src addresses and to
using loopback for dst addresses.

A port cannot appear more than once per network across the
src addresses of the pairs, i.e. a src port can appear at
most once for the host network and at most once for the
overlay network.`,
		Example: `  # will proxy connections on port 1234 on the overlay
  # network to addr 127.0.0.1:4321 on the host network
  net-sim tcp-proxy overlay-network.yml :1234 host::4321`,
		Args: cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return tcpProxy(args)
		},
	}
)

func init() {
	rootCmd.AddCommand(tcpProxyCmd)
}

func tcpProxy(args []string) error {
	hostTransport := hostnetwork.NewTransportLayer()
	ctx, cancel := newProcessContext()
	defer cancel()

	// create overlay network and transport from config
	overlayNetworkConfFile := args[0]
	overlayNetwork, err := network.NewLayerFromConfigFile(ctx, overlayNetworkConfFile)
	if err != nil {
		return err
	}
	overlayTransport := transport.NewLayer(overlayNetwork)

	// create host and overlay networks api map
	transportMap := map[string]transport.Layer{
		host:    hostTransport,
		overlay: overlayTransport,
	}

	// create listeners
	tcpProxyEntries, err := tcpProxyEntries(args[1:]).entries()
	if err != nil {
		return err
	}
	listeners := make([]net.Listener, 0, len(tcpProxyEntries))
	for i, entry := range tcpProxyEntries {
		t := transportMap[entry.src.networkOrDefault()]
		l, err := t.Listen(ctx, transport.TCP, entry.src.hostPort().String())
		if err != nil {
			for j := i - 1; 0 <= j; j-- {
				listeners[j].Close()
			}
			return fmt.Errorf("error listening on address '%s': %w", entry.src, err)
		}
		listeners = append(listeners, l)
	}

	// start proxy threads
	type (
		pureWriter struct{ io.Writer }
		pureReader struct{ io.Reader }
	)
	copyStream := func(dst io.Writer, src io.Reader) (buf *bytes.Buffer, err error) {
		// capture stream from src with a tee and print debug log
		tee := src
		if debug {
			buf = &bytes.Buffer{}
			tee = io.TeeReader(src, buf)
		}

		// copy stream from tee(src) to dst
		if _, err := io.Copy(&pureWriter{dst}, &pureReader{tee}); err != nil {
			if !transport.IsUseOfClosedConn(err) &&
				!errors.Is(err, context.Canceled) {
				return buf, err
			}
		}
		return buf, nil
	}
	var wg sync.WaitGroup
	for i, lis := range listeners {
		lis := lis
		entry := tcpProxyEntries[i]
		dstTransport := transportMap[entry.dst.networkOrDefault()]
		dstAddr := entry.dst.hostPort().orLoopback().String()

		wg.Add(1)
		go func() {
			defer wg.Done()

			l := logrus.
				WithField("src_addr", entry.src).
				WithField("dst_addr", entry.dst)

			var wg2 sync.WaitGroup
			defer wg2.Wait()
			for {
				// accept conn
				client, err := lis.Accept()
				if err != nil {
					if !transport.IsUseOfClosedConn(err) {
						l.
							WithError(err).
							Error("error accepting client connection")
					}
					return
				}
				l := l.
					WithField("client_local_addr", client.LocalAddr().String()).
					WithField("client_remote_addr", client.RemoteAddr().String())

				// connect to dst address
				server, err := dstTransport.Dial(ctx, transport.TCP, dstAddr)
				if err != nil {
					l.
						WithError(err).
						Error("error dialing to server")
					client.Close()
					continue // maybe transient
				}
				l = l.
					WithField("server_local_addr", server.LocalAddr().String()).
					WithField("server_remote_addr", server.RemoteAddr().String())

				// start proxy thread
				wg2.Add(1)
				go func() {
					defer func() {
						server.Close()
						client.Close()
						wg2.Done()
					}()

					var wg3 sync.WaitGroup
					defer wg3.Wait()

					// client -> server
					wg3.Add(1)
					go func() {
						defer func() {
							server.Close()
							wg3.Done()
						}()
						buf, err := copyStream(server, client)
						if err != nil {
							l.
								WithError(err).
								Error("error copying stream from client to server")
						}
						if debug {
							l.
								WithField("string", buf.String()).
								Debug("stream from client to server")
						}
					}()

					// server -> client
					wg3.Add(1)
					go func() {
						defer func() {
							client.Close()
							wg3.Done()
						}()
						buf, err := copyStream(client, server)
						if err != nil {
							l.
								WithError(err).
								Error("error copying stream from server to client")
						}
						if debug {
							l.
								WithField("string", buf.String()).
								Debug("stream from server to client")
						}
					}()
				}()
			}
		}()
	}

	// wait for ctx and close
	<-ctx.Done()
	closers := make([]io.Closer, len(listeners))
	for i, lis := range listeners {
		closers[i] = lis
	}
	err = pkgio.Close(closers...)
	wg.Wait()
	if cErr := pkgio.Close(overlayTransport, overlayNetwork, hostTransport); cErr != nil {
		err = multierror.Append(err, cErr)
	}
	return err
}
