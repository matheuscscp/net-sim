package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "net-sim",
	Short: "net-sim is a networking simulator for learning purposes",
}

func Execute() error {
	return rootCmd.Execute()
}

func contextWithCancelOnInterrupt(ctx context.Context) (context.Context, context.CancelFunc) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		<-sigs
		signal.Stop(sigs)
		close(sigs)
		for range sigs {
		}
	}()
	return ctx, cancel
}
