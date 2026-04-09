package orchestrator

import (
	"fmt"
	"strings"
	"sync"
)

const (
	maxHistoryRounds = 3
	responseTruncLen = 200
)

type ConvoResponse struct {
	Speaker string
	GuestID string
	Text    string
}

type ConvoRound struct {
	UserMessage string
	Responses   []ConvoResponse
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

	cb.current[key] = &ConvoRound{UserMessage: text}
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

// FormatHistory renders rounds as a readable prompt section with truncated responses.
func FormatHistory(rounds []*ConvoRound) string {
	if len(rounds) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Recent conversation (most recent first)\n\n")

	for i, round := range rounds {
		fmt.Fprintf(&sb, "### Round %d", len(rounds)-i)
		if i == 0 {
			sb.WriteString(" (latest)")
		}
		sb.WriteString("\n")
		fmt.Fprintf(&sb, "User: %q\n", round.UserMessage)
		for _, resp := range round.Responses {
			text := resp.Text
			if len(text) > responseTruncLen {
				text = text[:responseTruncLen] + "..."
			}
			fmt.Fprintf(&sb, "%s: %q\n", resp.Speaker, text)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
