package orchestrator

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	maxHistoryRounds      = 10
	defaultDisplayRounds  = 3
	defaultTruncLen       = 200
	expandedDisplayRounds = 10
	expandedTruncLen      = 800
)

type ConvoResponse struct {
	Speaker string
	GuestID string
	Text    string
}

type ConvoRound struct {
	UserMessage string
	Responses   []ConvoResponse
	// CreatedAt is the wall-clock moment the user message that
	// opened this round was recorded. Populated by
	// RecordUserMessage and consumed by HistoryBefore so chained
	// runs can filter out messages that arrived after the run
	// started (plan decision D12). Zero for rounds created before
	// timestamping landed -- HistoryBefore treats zero as "include"
	// to preserve legacy behavior.
	CreatedAt time.Time
}

type ConvoBuffer struct {
	mu      sync.Mutex
	history map[string][]*ConvoRound
	current map[string]*ConvoRound
}

func NewConvoBuffer() *ConvoBuffer {
	return &ConvoBuffer{
		history: make(map[string][]*ConvoRound),
		current: make(map[string]*ConvoRound),
	}
}

// RecordUserMessage finalizes the current round into history and starts a new round.
func (cb *ConvoBuffer) RecordUserMessage(key, text string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cur, ok := cb.current[key]; ok && cur.UserMessage != "" {
		hist := cb.history[key]
		hist = append(hist, cur)
		if len(hist) > maxHistoryRounds {
			hist = hist[len(hist)-maxHistoryRounds:]
		}
		cb.history[key] = hist
	}

	cb.current[key] = &ConvoRound{UserMessage: text, CreatedAt: time.Now()}
}

// HistoryBefore returns the same rounds as History but filters out
// any round whose CreatedAt is after cutoff. Zero cutoff disables the
// filter (matches History's semantics), which is what non-execute
// callers pass in. Zero CreatedAt rounds (pre-D12 entries) always
// pass the filter so legacy fixtures keep rendering.
func (cb *ConvoBuffer) HistoryBefore(key string, cutoff time.Time) []*ConvoRound {
	rounds := cb.History(key)
	if cutoff.IsZero() {
		return rounds
	}
	out := make([]*ConvoRound, 0, len(rounds))
	for _, r := range rounds {
		if r == nil {
			continue
		}
		if !r.CreatedAt.IsZero() && !r.CreatedAt.Before(cutoff) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// RecordResponse appends a response to the current round.
func (cb *ConvoBuffer) RecordResponse(key, speaker, guestID, text string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cur := cb.current[key]
	if cur == nil {
		cur = &ConvoRound{}
		cb.current[key] = cur
	}
	cur.Responses = append(cur.Responses, ConvoResponse{
		Speaker: speaker,
		GuestID: guestID,
		Text:    text,
	})
}

// History returns up to 3 previous completed rounds (most recent first).
func (cb *ConvoBuffer) History(key string) []*ConvoRound {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	hist := cb.history[key]
	if len(hist) == 0 {
		return nil
	}

	result := make([]*ConvoRound, len(hist))
	for i, r := range hist {
		result[len(hist)-1-i] = r
	}
	return result
}

// FormatHistory renders rounds as a readable prompt section with default truncation (3 rounds, 200 chars).
func FormatHistory(rounds []*ConvoRound) string {
	return FormatHistoryN(rounds, defaultDisplayRounds, defaultTruncLen)
}

// FormatHistoryN renders up to maxRounds rounds with responses truncated to truncLen chars.
func FormatHistoryN(rounds []*ConvoRound, maxRounds, truncLen int) string {
	if len(rounds) == 0 {
		return ""
	}

	display := rounds
	if len(display) > maxRounds {
		display = display[:maxRounds]
	}

	var sb strings.Builder
	sb.WriteString("## Recent conversation (most recent first)\n\n")

	for i, round := range display {
		fmt.Fprintf(&sb, "### Round %d", len(display)-i)
		if i == 0 {
			sb.WriteString(" (latest)")
		}
		sb.WriteString("\n")
		fmt.Fprintf(&sb, "User: %q\n", round.UserMessage)
		for _, resp := range round.Responses {
			text := resp.Text
			if len(text) > truncLen {
				text = text[:truncLen] + "..."
			}
			fmt.Fprintf(&sb, "%s: %q\n", resp.Speaker, text)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// HasGuestSpoken returns true if the given guest has any recorded response in
// the history or the current round for the channel key.
func (cb *ConvoBuffer) HasGuestSpoken(key, guestID string) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	for _, round := range cb.history[key] {
		for _, resp := range round.Responses {
			if resp.GuestID == guestID {
				return true
			}
		}
	}
	if cur, ok := cb.current[key]; ok {
		for _, resp := range cur.Responses {
			if resp.GuestID == guestID {
				return true
			}
		}
	}
	return false
}
