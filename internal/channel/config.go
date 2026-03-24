package channel

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type ChannelsConfig struct {
	Slack *SlackCredentials `yaml:"slack,omitempty"`
}

type SlackCredentials struct {
	BotToken string `yaml:"bot_token"`
	AppToken string `yaml:"app_token"`
}

func LoadCredentials(path string) (*ChannelsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading channels config: %w", err)
	}

	var cfg ChannelsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing channels config: %w", err)
	}

	return &cfg, nil
}

func DefaultCredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".anthem", "channels.yaml"), nil
}
