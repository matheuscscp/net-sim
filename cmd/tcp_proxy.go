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

var tcpProxyCmd = &cobra.Command{
	Use:   "tcp-proxy <network-layer-yaml-config-file> <<host-port> <overlay-port>>...",
	Short: "Proxy TCP connections from the host network to the overlay netowrk",
	Args:  cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		// read network layer config
		confFile := args[0]
		b, err := os.ReadFile(confFile)
		if err != nil {
			return fmt.Errorf("error reading yaml network layer config file: %w", err)
		}
		var networkLayerConf network.LayerConfig
		if err := yaml.Unmarshal(b, &networkLayerConf); err != nil {
			return fmt.Errorf("error decoding network layer config from yaml: %w", err)
		}

		// create host listeners (bind host ports)
		ports := args[1:]
		if len(ports)%2 == 1 {
			return errors.New("number of specified ports is not even")
		}
		listeners := make([]net.Listener, 0, len(ports)/2)
		for i := 0; i < len(ports); i += 2 {
			hostPort := ports[i]
			l, err := net.Listen(transport.TCP, fmt.Sprintf(":%s", hostPort))
			if err != nil {
				for j := i - 1; 0 <= j; j-- {
					listeners[j].Close()
				}
				return fmt.Errorf("error listening on host port %s: %w", hostPort, err)
			}
			listeners = append(listeners, l)
		}

		// create ctx
		ctx, cancel := contextWithCancelOnInterrupt(context.Background())
		defer cancel()

		// create overlay network and transport layers
		networkLayer, err := network.NewLayer(ctx, networkLayerConf)
		if err != nil {
			return err
		}
		transportLayer := transport.NewLayer(networkLayer)

		// start threads
		var wg sync.WaitGroup
		for i, lis := range listeners {
			i := i * 2
			lis := lis
			wg.Add(1)
			go func() {
				defer wg.Done()

				hostPort, overlayPort := ports[i], ports[i+1]
				l := logrus.
					WithField("host_port", hostPort).
					WithField("overlay_port", overlayPort)

				var wg2 sync.WaitGroup
				defer wg2.Wait()
				for {
					// accept conn on host port
					client, err := lis.Accept()
					if err != nil {
						if !strings.Contains(err.Error(), "use of closed network connection") {
							l.
								WithError(err).
								Error("error accepting connection on host port")
						}
						return
					}
					l := l.
						WithField("client_local_addr", client.LocalAddr().String()).
						WithField("client_remote_addr", client.RemoteAddr().String())

					// connect to overlay port
					remoteHost, _, _ := net.SplitHostPort(client.RemoteAddr().String())
					remoteAddr := fmt.Sprintf("%s:%s", remoteHost, overlayPort)
					server, err := transportLayer.Dial(ctx, transport.TCP, remoteAddr)
					if err != nil {
						l.
							WithError(err).
							Error("error dialing to server on overlay port")
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
									Error("error copying from client to server")
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
									Error("error copying from server to client")
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
		transportLayer.Close()
		networkLayer.Close()

		// drain remaining data
		for _, intf := range networkLayer.Interfaces() {
			for range intf.Recv() {
			}
			if intf.Card() != nil { // loopback
				for range intf.Card().Recv() {
				}
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(tcpProxyCmd)
}
