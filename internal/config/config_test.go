package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultConfig_ChatMaxTurns(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Orchestrator.ChatMaxTurns != 2 {
		t.Errorf("Orchestrator.ChatMaxTurns = %d, want 2", cfg.Orchestrator.ChatMaxTurns)
	}
}

func TestChatMaxTurns_UnmarshalsFromYAML(t *testing.T) {
	raw := []byte("enabled: true\nchat_max_turns: 7\n")
	var oc OrchestratorConfig
	if err := yaml.Unmarshal(raw, &oc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if oc.ChatMaxTurns != 7 {
		t.Errorf("ChatMaxTurns = %d, want 7", oc.ChatMaxTurns)
	}
}
