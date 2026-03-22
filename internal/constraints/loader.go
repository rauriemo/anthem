package constraints

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type ConstraintsConfig struct {
	Constraints []string `yaml:"constraints"`
	Loaded      bool
}

func LoadFile(path string) (*ConstraintsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &ConstraintsConfig{Loaded: false}, nil
		}
		return nil, fmt.Errorf("reading constraints file: %w", err)
	}

	var cc ConstraintsConfig
	if err := yaml.Unmarshal(data, &cc); err != nil {
		return nil, fmt.Errorf("parsing constraints file: %w", err)
	}
	cc.Loaded = true
	return &cc, nil
}

func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".anthem", "constraints.yaml"), nil
}
