package cmd

import (
	"github.com/matheuscscp/net-sim/internal/layers/link"

	"github.com/spf13/cobra"
)

func Execute() error {
	rootCmd := &cobra.Command{
		Use:   "net-sim",
		Short: "net-sim is a networking simulator for learning purposes",
	}

	switchCmd := &cobra.Command{
		Use:   "switch <spec-file>",
		Short: "switch simulates an L2 switch",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return link.RunSwitch(args[0])
		},
	}

	rootCmd.AddCommand(switchCmd)
	return rootCmd.Execute()
}
