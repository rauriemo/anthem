package orchestrator

import "testing"

func TestSharedContext_GetEmpty(t *testing.T) {
	sc := NewSharedContext()
	if got := sc.Get("missing"); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestSharedContext_UpdateAndGet(t *testing.T) {
	sc := NewSharedContext()
	sc.Update("prism", "Key decisions: use card system with progression")
	if got := sc.Get("prism"); got != "Key decisions: use card system with progression" {
		t.Errorf("unexpected value: %q", got)
	}
}

func TestSharedContext_Overwrite(t *testing.T) {
	sc := NewSharedContext()
	sc.Update("prism", "original")
	sc.Update("prism", "updated")
	if got := sc.Get("prism"); got != "updated" {
		t.Errorf("expected 'updated', got %q", got)
	}
}

func TestSharedContext_MultipleChannels(t *testing.T) {
	sc := NewSharedContext()
	sc.Update("prism", "prism context")
	sc.Update("slack", "slack context")

	if got := sc.Get("prism"); got != "prism context" {
		t.Errorf("prism context wrong: %q", got)
	}
	if got := sc.Get("slack"); got != "slack context" {
		t.Errorf("slack context wrong: %q", got)
	}
}
