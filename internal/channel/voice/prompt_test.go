package voice

import (
	"strings"
	"testing"
)

func TestApplyVoiceModifier(t *testing.T) {
	t.Parallel()
	original := "You are an orchestrator agent."
	result := ApplyVoiceModifier(original)

	if !strings.HasPrefix(result, original) {
		t.Error("modified prompt should start with original")
	}
	if !strings.Contains(result, "voice mode") {
		t.Error("modified prompt should mention voice mode")
	}
	if !strings.Contains(result, "concise") {
		t.Error("modified prompt should instruct conciseness")
	}
	if !strings.Contains(result, "Front-load") {
		t.Error("modified prompt should instruct front-loading")
	}
	if !strings.Contains(result, "interrupted") {
		t.Error("modified prompt should mention interruption handling")
	}
}

func TestApplyVoiceModifier_TrailingNewlines(t *testing.T) {
	t.Parallel()
	original := "Some prompt\n\n\n"
	result := ApplyVoiceModifier(original)

	if strings.Contains(result, "\n\n\n\n") {
		t.Error("should not accumulate excessive newlines")
	}
}

func TestRecoveryModifier_Integration(t *testing.T) {
	t.Parallel()
	ir := NewInterruptionRecovery()
	ir.Capture("I was explaining the path system", "t1", "orch")

	modified := RecoveryModifier("What about the enemies?", ir)

	if !strings.Contains(modified, "path system") {
		t.Error("should include partial response context")
	}
	if !strings.Contains(modified, "What about the enemies?") {
		t.Error("should include user message")
	}

	second := RecoveryModifier("Another question", ir)
	if second != "Another question" {
		t.Errorf("second call should have no recovery, got: %q", second)
	}
}
