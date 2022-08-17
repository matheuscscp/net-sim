package main

import (
	"os"

	"github.com/matheuscscp/net-sim/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
