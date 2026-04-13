package voice

import (
	"strings"
	"testing"
)

func TestInterruptionRecovery_NoPending(t *testing.T) {
	t.Parallel()
	ir := NewInterruptionRecovery()

	if ir.HasPending() {
		t.Error("should not have pending on init")
	}
	if prefix := ir.RecoveryPrefix(); prefix != "" {
		t.Errorf("prefix = %q, want empty", prefix)
	}
}

func TestInterruptionRecovery_CaptureAndRecover(t *testing.T) {
	t.Parallel()
	ir := NewInterruptionRecovery()

	ir.Capture("I was saying that the goblin sprites need", "thread-1", "orchestrator")

	if !ir.HasPending() {
		t.Error("should have pending after capture")
	}

	prefix := ir.RecoveryPrefix()
	if prefix == "" {
		t.Fatal("expected non-empty recovery prefix")
	}
	if !strings.Contains(prefix, "goblin sprites") {
		t.Errorf("prefix should contain partial response, got: %s", prefix)
	}
	if !strings.Contains(prefix, "interrupted") {
		t.Errorf("prefix should mention interruption, got: %s", prefix)
	}

	if ir.HasPending() {
		t.Error("should not have pending after RecoveryPrefix consumed it")
	}
}

func TestInterruptionRecovery_EmptyPartial(t *testing.T) {
	t.Parallel()
	ir := NewInterruptionRecovery()

	ir.Capture("", "thread-1", "orchestrator")

	prefix := ir.RecoveryPrefix()
	if prefix != "" {
		t.Errorf("empty partial should produce empty prefix, got: %q", prefix)
	}
}

func TestInterruptionRecovery_Clear(t *testing.T) {
	t.Parallel()
	ir := NewInterruptionRecovery()

	ir.Capture("Some partial", "t1", "orch")
	ir.Clear()

	if ir.HasPending() {
		t.Error("should not have pending after clear")
	}
}

func TestInterruptionRecovery_Truncation(t *testing.T) {
	t.Parallel()
	ir := NewInterruptionRecovery()

	long := strings.Repeat("a", 300)
	ir.Capture(long, "t1", "orch")

	prefix := ir.RecoveryPrefix()
	if len(prefix) > 400 {
		t.Errorf("prefix too long (%d chars), should truncate partial", len(prefix))
	}
}

func TestRecoveryModifier_WithRecovery(t *testing.T) {
	t.Parallel()
	ir := NewInterruptionRecovery()
	ir.Capture("partial response here", "t1", "orch")

	result := RecoveryModifier("New user message", ir)
	if !strings.Contains(result, "partial response here") {
		t.Error("modified message should contain recovery prefix")
	}
	if !strings.Contains(result, "New user message") {
		t.Error("modified message should contain original message")
	}
}

func TestRecoveryModifier_WithoutRecovery(t *testing.T) {
	t.Parallel()
	ir := NewInterruptionRecovery()

	result := RecoveryModifier("New user message", ir)
	if result != "New user message" {
		t.Errorf("without recovery, message should be unchanged, got: %q", result)
	}
}
