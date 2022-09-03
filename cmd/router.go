package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/transport"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var routerCmd = &cobra.Command{
	Use:   "router <yaml-config-file>",
	Short: "Simulate a router on the overlay network",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// read config
		b, err := os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("error reading yaml router config file: %w", err)
		}
		var conf struct {
			Interfaces []network.InterfaceConfig `yaml:"interfaces"`
			Routes     []network.RouteConfig     `yaml:"routes"`
		}
		if err := yaml.Unmarshal(b, &conf); err != nil {
			return fmt.Errorf("error decoding router config from yaml: %w", err)
		}

		// create ctx
		ctx, cancel := contextWithCancelOnInterrupt(context.Background())
		defer cancel()

		// create network layer
		networkLayer, err := network.NewLayer(ctx, network.LayerConfig{
			ForwardingMode: true,
			Interfaces:     conf.Interfaces,
		})
		if err != nil {
			return err
		}
		if err := networkLayer.ForwardingTable().StoreRoutesFromConfig(conf.Routes); err != nil {
			return err
		}

		// create transport layer
		transportLayer := transport.NewLayer(networkLayer)

		// TODO: run application layer servers (DHCP, DNS, ICMP, NAT, BGP)

		// wait for ctx and close
		<-ctx.Done()
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
	rootCmd.AddCommand(routerCmd)
}
