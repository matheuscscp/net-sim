package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	logLevel  string
	logFormat string
	rootCmd   = &cobra.Command{
		Use:   "net-sim",
		Short: "net-sim is a networking simulator for learning purposes",
		PersistentPreRunE: func(*cobra.Command, []string) error {
			return setupLogger()
		},
	}
	debug bool
)

func init() {
	rootCmd.
		PersistentFlags().
		StringVarP(&logLevel, "log-level", "v", "", "log level in logrus.Level format")
	rootCmd.
		PersistentFlags().
		StringVarP(&logFormat, "log-format", "f", "text", `"text" or "json"`)
}

func setupLogger() error {
	// set level
	if logLevel == "" {
		return nil
	}
	lvl, err := logrus.ParseLevel(logLevel)
	if err != nil {
		return err
	}
	logrus.SetLevel(lvl)
	if lvl >= logrus.DebugLevel {
		debug = true
	}

	// set format
	if logFormat == "text" {
		logrus.SetFormatter(&logrus.TextFormatter{
			TimestampFormat: time.RFC3339,
			FullTimestamp:   true,
		})
	}
	switch logFormat {
	case "", "text":
		logrus.SetFormatter(&logrus.TextFormatter{
			TimestampFormat: time.RFC3339,
			FullTimestamp:   true,
		})
	case "json":
		logrus.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: time.RFC3339,
		})
	default:
		return fmt.Errorf("invalid log format: %v", logFormat)
	}
	return nil
}

func Execute() error {
	return rootCmd.Execute()
}

func contextWithCancelOnInterrupt() (context.Context, context.CancelFunc) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()
		defer signal.Stop(sigs)
		select {
		case <-sigs:
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
