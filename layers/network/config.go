package network

import (
	"context"
	"fmt"

	"github.com/matheuscscp/net-sim/config"
)

func NewLayerFromConfigFile(ctx context.Context, file string) (Layer, error) {
	var conf LayerConfig
	if err := config.ReadYAMLFileAndUnmarshal(file, &conf); err != nil {
		return nil, fmt.Errorf("error reading yaml overlay network config file: %w", err)
	}
	l, err := NewLayer(ctx, conf)
	if err != nil {
		return nil, err
	}
	return l, nil
}
