package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	logLevel    string
	logFormat   string
	metricsAddr string
	rootCmd     = &cobra.Command{
		Use:   "net-sim",
		Short: "net-sim is a networking simulator for learning purposes",
		PersistentPreRunE: func(*cobra.Command, []string) error {
			return setupLogger()
		},
	}
	debug bool
)

func init() {
	persistentFlags := rootCmd.PersistentFlags()
	persistentFlags.StringVar(&logLevel, "log-level", "info", "log level in logrus.Level format")
	persistentFlags.StringVar(&logFormat, "log-format", "text", `"text" or "json"`)
	persistentFlags.StringVar(&metricsAddr, "metrics-addr", "", "address for /metrics HTTP endpoint")
}

func setupLogger() error {
	// set level
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

func newProcessContext() (context.Context, context.CancelFunc) {
	wg := &sync.WaitGroup{}
	ctx, cancel := contextWithCancelOnInterrupt(wg)
	setupMetrics(ctx, wg)
	return ctx, func() {
		cancel()
		wg.Wait()
	}
}

func contextWithCancelOnInterrupt(wg *sync.WaitGroup) (context.Context, context.CancelFunc) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	ctx, cancel := context.WithCancel(context.Background())
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		defer signal.Stop(sigs)
		select {
		case <-sigs:
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func setupMetrics(ctx context.Context, wg *sync.WaitGroup) {
	s := &http.Server{
		Addr: metricsAddr,
		Handler: promhttp.InstrumentMetricHandler(
			prometheus.DefaultRegisterer, promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{
				EnableOpenMetrics: true,
			}),
		),
	}

	// listen and serve metrics endpoint on a separate go routine
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logrus.
				WithError(err).
				Error("error listening and serving metrics")
		}
	}()

	// block on <-ctx.Done() and shutdown metrics server on a separate go routine
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			logrus.
				WithError(err).
				Error("error shutting down metrics server")
		}
	}()
}
