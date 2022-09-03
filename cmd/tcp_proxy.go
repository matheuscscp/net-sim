package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/transport"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	tcpProxyCmd = &cobra.Command{
		Use:   "tcp-proxy",
		Short: "Proxy TCP connections between the host network and the overlay netowrk",
	}

	tcpProxyReverseCmd = &cobra.Command{
		Use:   "reverse <overlay-network-yaml-config-file> <<host-addr> <overlay-addr>>...",
		Short: "Proxy TCP connections from the host network to the overlay netowrk",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return tcpProxy(args, true /*reverse*/)
		},
	}

	tcpProxyForwardCmd = &cobra.Command{
		Use:   "forward <overlay-network-yaml-config-file> <<overlay-addr> <host-addr>>...",
		Short: "Proxy TCP connections from the overlay network to the host netowrk",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return tcpProxy(args, false /*reverse*/)
		},
	}
)

func init() {
	tcpProxyCmd.AddCommand(tcpProxyReverseCmd)
	tcpProxyCmd.AddCommand(tcpProxyForwardCmd)
	rootCmd.AddCommand(tcpProxyCmd)
}

func tcpProxy(args []string, reverse bool) error {
	hostTransport := transport.NewHostLayer()
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

	// check proxy mode
	hostLabel, overlayLabel := "host", "overlay"
	hostAddrLabel, overlayAddrLabel := fmt.Sprintf("%s_addr", hostLabel), fmt.Sprintf("%s_addr", overlayLabel)
	if !reverse { // forward
		hostTransport, overlayTransport = overlayTransport, hostTransport
		hostAddrLabel, overlayAddrLabel = overlayAddrLabel, hostAddrLabel
		hostLabel, overlayLabel = overlayLabel, hostLabel
	}

	// create host listeners (bind host addrs)
	addrs := args[1:]
	if len(addrs)%2 == 1 {
		return errors.New("number of specified addresses is not even")
	}
	listeners := make([]net.Listener, 0, len(addrs)/2)
	for i := 0; i < len(addrs); i += 2 {
		hostAddr := addrs[i]
		l, err := hostTransport.Listen(transport.TCP, hostAddr)
		if err != nil {
			for j := i - 1; 0 <= j; j-- {
				listeners[j].Close()
			}
			return fmt.Errorf("error listening on %s address '%s': %w", hostLabel, hostAddr, err)
		}
		listeners = append(listeners, l)
	}

	// start threads
	var wg sync.WaitGroup
	for i, lis := range listeners {
		i := i * 2
		lis := lis
		wg.Add(1)
		go func() {
			defer wg.Done()

			hostAddr, overlayAddr := addrs[i], addrs[i+1] // the command usage already swaps these args
			l := logrus.
				WithField(hostAddrLabel, hostAddr).
				WithField(overlayAddrLabel, overlayAddr)

			var wg2 sync.WaitGroup
			defer wg2.Wait()
			for {
				// accept conn on host address
				client, err := lis.Accept()
				if err != nil {
					if !strings.Contains(err.Error(), "use of closed network connection") {
						l.
							WithError(err).
							Errorf("error accepting client connection on %s network", hostLabel)
					}
					return
				}
				l := l.
					WithField("client_local_addr", client.LocalAddr().String()).
					WithField("client_remote_addr", client.RemoteAddr().String())

				// connect to overlay address
				server, err := overlayTransport.Dial(ctx, transport.TCP, overlayAddr)
				if err != nil {
					l.
						WithError(err).
						Errorf("error dialing to server on %s network", overlayLabel)
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
						defer wg3.Done()
						if _, err := io.Copy(server, client); err != nil {
							l.
								WithError(err).
								Errorf("error copying from client to server (from %s to %s)", hostLabel, overlayLabel)
						}
						server.Close()
					}()

					// server -> client
					wg3.Add(1)
					go func() {
						defer wg3.Done()
						if _, err := io.Copy(client, server); err != nil {
							l.
								WithError(err).
								Errorf("error copying from server to client (from %s to %s)", overlayLabel, hostLabel)
						}
						client.Close()
					}()
				}()
			}
		}()
	}

	// wait for ctx and close
	<-ctx.Done()
	for _, lis := range listeners {
		lis.Close()
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
