package cmd

import (
	"context"
	"fmt"

	"github.com/matheuscscp/net-sim/config"
	"github.com/matheuscscp/net-sim/layers/network"
	"github.com/matheuscscp/net-sim/layers/transport"

	"github.com/spf13/cobra"
)

var routerCmd = &cobra.Command{
	Use:   "router <yaml-config-file>",
	Short: "Simulate a router on the overlay network",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// read config
		var conf struct {
			Interfaces []network.InterfaceConfig `yaml:"interfaces"`
			Routes     []network.RouteConfig     `yaml:"routes"`
		}
		if err := config.ReadYAML(args[0], &conf); err != nil {
			return fmt.Errorf("error reading yaml router config file: %w", err)
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

		return nil
	},
}

func init() {
	rootCmd.AddCommand(routerCmd)
}
