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
	Short: "router simulates a router",
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

		// create network layer
		ctx, cancel := contextWithCancelOnInterrupt(context.Background())
		defer cancel()
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

		// TODO: run application layer servers (DNS, DHCP, BGP, NAT)

		// wait interruption signals and close
		<-ctx.Done()
		transportLayer.Close()
		networkLayer.Close()

		// drain remaining datagrams
		for _, intf := range networkLayer.Interfaces() {
			for range intf.Recv() {
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(routerCmd)
}
