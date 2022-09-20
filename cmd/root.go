package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "net-sim",
	Short: "net-sim is a networking simulator for learning purposes",
}

func init() {
	logrus.SetFormatter(&logrus.TextFormatter{
		TimestampFormat: time.RFC3339,
		FullTimestamp:   true,
	})
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
	}()
	return ctx, cancel
}
