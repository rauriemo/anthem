package channel

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type ChannelsConfig struct {
	Slack    *SlackCredentials    `yaml:"slack,omitempty"`
	Dispatch *DispatchCredentials `yaml:"dispatch,omitempty"`
	Prism    *PrismCredentials    `yaml:"prism,omitempty"`
	Voice    *VoiceCredentials    `yaml:"voice,omitempty"`
}

type DispatchCredentials struct {
	Token string `yaml:"token"`
}

type SlackCredentials struct {
	BotToken string `yaml:"bot_token"`
	AppToken string `yaml:"app_token"`
}

type PrismCredentials struct {
	Token string `yaml:"token"`
}

type VoiceCredentials struct {
	LiveKitURL       string `yaml:"livekit_url"`
	LiveKitAPIKey    string `yaml:"livekit_api_key"`
	LiveKitAPISecret string `yaml:"livekit_api_secret"`
	DeepgramAPIKey   string `yaml:"deepgram_api_key"`
	ElevenLabsAPIKey string `yaml:"elevenlabs_api_key,omitempty"`
	AnthropicAPIKey  string `yaml:"anthropic_api_key,omitempty"`
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
