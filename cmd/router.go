package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/matheuscscp/net-sim/layers/network"

	gplayers "github.com/google/gopacket/layers"
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
		l, err := network.NewLayer(ctx, network.LayerConfig{
			ForwardingMode: true,
			Interfaces:     conf.Interfaces,
		})
		if err != nil {
			return err
		}
		if err := l.ForwardingTable().StoreRoutesFromConfig(conf.Routes); err != nil {
			return err
		}

		// listen with cancel on interruption signals
		l.Listen(ctx, func(datagram *gplayers.IPv4) {
			// TODO: notify transport layer
		})

		l.Close()

		// drain remaining datagrams
		for _, intf := range l.Interfaces() {
			for range intf.Recv() {
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(routerCmd)
}
