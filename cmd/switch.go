package cmd

import (
	"context"
	"fmt"

	"github.com/matheuscscp/net-sim/config"
	"github.com/matheuscscp/net-sim/layers/link"

	"github.com/spf13/cobra"
)

var switchCmd = &cobra.Command{
	Use:   "switch <yaml-config-file>",
	Short: "Simulate an L2 switch on the overlay network",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// read config
		var conf link.SwitchConfig
		if err := config.ReadYAMLFileAndUnmarshal(args[0], &conf); err != nil {
			return fmt.Errorf("error reading yaml router config file: %w", err)
		}

		// start switch with cancel on interruption signals
		ctx, cancel := contextWithCancelOnInterrupt(context.Background())
		defer cancel()
		waitClose, err := link.RunSwitch(ctx, conf)
		if err != nil {
			return err
		}

		waitClose(nil)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(switchCmd)
}
