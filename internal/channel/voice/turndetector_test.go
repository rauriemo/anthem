package voice

import (
	"testing"
	"time"
)

func TestTurnDetector_OnFinal_Commit(t *testing.T) {
	t.Parallel()
	td := NewTurnDetector(300*time.Millisecond, 2)

	decision := td.OnFinal("Hello there", 0.95)
	if decision != TurnCommit {
		t.Errorf("decision = %v, want TurnCommit", decision)
	}
}

func TestTurnDetector_OnFinal_DiscardShort(t *testing.T) {
	t.Parallel()
	td := NewTurnDetector(300*time.Millisecond, 5)

	decision := td.OnFinal("Hi", 0.95)
	if decision != TurnDiscard {
		t.Errorf("decision = %v, want TurnDiscard for short utterance", decision)
	}
}

func TestTurnDetector_OnFinal_DiscardLowConfidence(t *testing.T) {
	t.Parallel()
	td := NewTurnDetector(300*time.Millisecond, 2)

	decision := td.OnFinal("some noise", 0.1)
	if decision != TurnDiscard {
		t.Errorf("decision = %v, want TurnDiscard for low confidence", decision)
	}
}

func TestTurnDetector_OnPartial_Continue(t *testing.T) {
	t.Parallel()
	td := NewTurnDetector(300*time.Millisecond, 2)

	decision := td.OnPartial("Hello")
	if decision != TurnContinue {
		t.Errorf("decision = %v, want TurnContinue", decision)
	}
	if td.PendingText() != "Hello" {
		t.Errorf("pending = %q, want Hello", td.PendingText())
	}
}

func TestTurnDetector_CheckSilenceTimeout(t *testing.T) {
	t.Parallel()
	td := NewTurnDetector(10*time.Millisecond, 2)

	td.OnPartial("Hello world")
	time.Sleep(20 * time.Millisecond)

	decision := td.CheckSilenceTimeout()
	if decision != TurnCommit {
		t.Errorf("decision = %v, want TurnCommit after timeout", decision)
	}
}

func TestTurnDetector_CheckSilenceTimeout_TooSoon(t *testing.T) {
	t.Parallel()
	td := NewTurnDetector(1*time.Second, 2)

	td.OnPartial("Hello world")

	decision := td.CheckSilenceTimeout()
	if decision != TurnContinue {
		t.Errorf("decision = %v, want TurnContinue (too soon)", decision)
	}
}

func TestTurnDetector_Reset(t *testing.T) {
	t.Parallel()
	td := NewTurnDetector(300*time.Millisecond, 2)

	td.OnPartial("Some text")
	td.Reset()

	if td.PendingText() != "" {
		t.Errorf("pending after reset = %q, want empty", td.PendingText())
	}

	decision := td.CheckSilenceTimeout()
	if decision != TurnContinue {
		t.Errorf("decision after reset = %v, want TurnContinue", decision)
	}
}

func TestTurnDetector_ZeroConfidenceCommits(t *testing.T) {
	t.Parallel()
	td := NewTurnDetector(300*time.Millisecond, 2)

	decision := td.OnFinal("Hello there", 0)
	if decision != TurnCommit {
		t.Errorf("zero confidence should commit (confidence not provided), got %v", decision)
	}
}

func TestTurnDetector_Defaults(t *testing.T) {
	t.Parallel()
	td := NewTurnDetector(0, 0)

	if td.silenceThreshold != 300*time.Millisecond {
		t.Errorf("default silence threshold = %v, want 300ms", td.silenceThreshold)
	}
	if td.minUtteranceLen != 2 {
		t.Errorf("default min utterance = %d, want 2", td.minUtteranceLen)
	}
}
