package voice

import (
	"strings"
	"time"
)

// TurnDetector provides semantic turn detection beyond simple silence-based VAD.
// It considers transcript content, pause duration, and acoustic signals to
// decide whether the user has finished their turn.
type TurnDetector struct {
	silenceThreshold time.Duration
	minUtteranceLen  int
	lastSpeechTime   time.Time
	pendingText      strings.Builder
}

// NewTurnDetector creates a detector with configurable thresholds.
// silenceThreshold: how long silence must last before committing a turn.
// minUtteranceLen: minimum character length to be considered a real utterance.
func NewTurnDetector(silenceThreshold time.Duration, minUtteranceLen int) *TurnDetector {
	if silenceThreshold == 0 {
		silenceThreshold = 300 * time.Millisecond
	}
	if minUtteranceLen == 0 {
		minUtteranceLen = 2
	}
	return &TurnDetector{
		silenceThreshold: silenceThreshold,
		minUtteranceLen:  minUtteranceLen,
	}
}

// TurnDecision represents the detector's judgment on whether a turn is complete.
type TurnDecision int

const (
	TurnContinue TurnDecision = iota // User is still speaking or pausing briefly
	TurnCommit                       // User has finished, commit the turn
	TurnDiscard                      // Not a real utterance (noise, filler)
)

// OnPartial processes a partial transcript. Returns TurnContinue.
func (td *TurnDetector) OnPartial(text string) TurnDecision {
	td.lastSpeechTime = time.Now()
	td.pendingText.Reset()
	td.pendingText.WriteString(text)
	return TurnContinue
}

// OnFinal processes a final transcript and decides whether to commit the turn.
func (td *TurnDetector) OnFinal(text string, confidence float64) TurnDecision {
	trimmed := strings.TrimSpace(text)

	if len(trimmed) < td.minUtteranceLen {
		td.pendingText.Reset()
		return TurnDiscard
	}

	if confidence > 0 && confidence < 0.3 {
		td.pendingText.Reset()
		return TurnDiscard
	}

	td.pendingText.Reset()
	return TurnCommit
}

// CheckSilenceTimeout returns TurnCommit if enough silence has passed since
// the last speech activity. Call this periodically from the adapter's tick loop.
func (td *TurnDetector) CheckSilenceTimeout() TurnDecision {
	if td.lastSpeechTime.IsZero() {
		return TurnContinue
	}
	if time.Since(td.lastSpeechTime) >= td.silenceThreshold {
		td.lastSpeechTime = time.Time{}
		if td.pendingText.Len() >= td.minUtteranceLen {
			return TurnCommit
		}
		return TurnDiscard
	}
	return TurnContinue
}

// Reset clears the detector state.
func (td *TurnDetector) Reset() {
	td.lastSpeechTime = time.Time{}
	td.pendingText.Reset()
}

// PendingText returns the current accumulated text.
func (td *TurnDetector) PendingText() string {
	return td.pendingText.String()
}
