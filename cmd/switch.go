package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/matheuscscp/net-sim/layers/link"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var switchCmd = &cobra.Command{
	Use:   "switch <yaml-config-file>",
	Short: "switch simulates an L2 switch",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// read config
		b, err := os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("error reading yaml switch config file: %w", err)
		}
		var conf link.SwitchConfig
		if err := yaml.Unmarshal(b, &conf); err != nil {
			return fmt.Errorf("error decoding switch config from yaml: %w", err)
		}

		// start switch
		ctx, cancel := contextWithCancelOnInterrupt(context.Background())
		defer cancel()
		waitClose, err := link.RunSwitch(ctx, conf)
		if err != nil {
			return err
		}

		waitClose()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(switchCmd)
}
