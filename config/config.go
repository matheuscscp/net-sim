package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func ReadYAML(file string, v interface{}) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("error reading yaml config file: %w", err)
	}
	if err := yaml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("error decoding config from yaml: %w", err)
	}
	return nil
}
