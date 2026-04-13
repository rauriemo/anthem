package voice

import (
	"strings"
	"testing"
)

func TestGetAckPhrase_KnownAgent(t *testing.T) {
	t.Parallel()
	phrase := GetAckPhrase("eiji", 0)
	if phrase != "Eiji here." {
		t.Errorf("phrase = %q, want 'Eiji here.'", phrase)
	}
}

func TestGetAckPhrase_RoundRobin(t *testing.T) {
	t.Parallel()
	p0 := GetAckPhrase("eiji", 0)
	p1 := GetAckPhrase("eiji", 1)
	p2 := GetAckPhrase("eiji", 2)

	if p0 == p1 && p1 == p2 {
		t.Error("round-robin should produce different phrases")
	}
}

func TestGetAckPhrase_UnknownAgent(t *testing.T) {
	t.Parallel()
	phrase := GetAckPhrase("unknown-agent", 0)
	if phrase != "unknown-agent here." {
		t.Errorf("phrase = %q, want 'unknown-agent here.'", phrase)
	}
}

func TestGetBridgePhrase(t *testing.T) {
	t.Parallel()
	phrase := GetBridgePhrase("Eiji", 0)
	if !strings.Contains(phrase, "Eiji") {
		t.Errorf("bridge phrase should contain agent name, got: %q", phrase)
	}
}

func TestGetBridgePhrase_RoundRobin(t *testing.T) {
	t.Parallel()
	phrases := make(map[string]bool)
	for i := 0; i < len(OrchestratorBridgePhrases); i++ {
		phrases[GetBridgePhrase("Test", i)] = true
	}
	if len(phrases) < 2 {
		t.Error("bridge phrases should vary")
	}
}

func TestDefaultAckTemplates_AllAgents(t *testing.T) {
	t.Parallel()
	expected := []string{"eiji", "miyazaki", "shigeru", "tolkien", "kaplan"}
	for _, agent := range expected {
		templates, ok := DefaultAckTemplates[agent]
		if !ok {
			t.Errorf("missing templates for agent %s", agent)
			continue
		}
		if len(templates) == 0 {
			t.Errorf("empty templates for agent %s", agent)
		}
	}
}
