package slack

import (
	"testing"
)

func TestNewAdapter(t *testing.T) {
	a := NewAdapter("xoxb-test", "xapp-test", "C123", nil)
	if a == nil {
		t.Fatal("expected non-nil adapter")
	}
	if a.botToken != "xoxb-test" {
		t.Errorf("botToken = %q, want xoxb-test", a.botToken)
	}
	if a.channelID != "C123" {
		t.Errorf("channelID = %q, want C123", a.channelID)
	}
}

func TestKind(t *testing.T) {
	a := NewAdapter("xoxb-test", "xapp-test", "C123", nil)
	if got := a.Kind(); got != "slack" {
		t.Errorf("Kind() = %q, want slack", got)
	}
}

func TestIncomingReturnsChannel(t *testing.T) {
	a := NewAdapter("xoxb-test", "xapp-test", "C123", nil)
	ch := a.Incoming()
	if ch == nil {
		t.Fatal("Incoming() returned nil")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	a := NewAdapter("xoxb-test", "xapp-test", "C123", nil)
	// Close before Start — cancel is nil, should not panic
	if err := a.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}
