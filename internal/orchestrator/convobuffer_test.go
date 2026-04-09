package orchestrator

import (
	"strings"
	"testing"
)

func TestConvoBuffer_RecordAndHistory(t *testing.T) {
	cb := NewConvoBuffer()
	key := "prism"

	cb.RecordUserMessage(key, "What should the card system look like?")
	cb.RecordResponse(key, "Game Designer", "game-designer", "I recommend a ScriptableObject-based architecture...")
	cb.RecordResponse(key, "RPG Writer", "rpg-writer", "Each card should tie to a story beat...")

	cb.RecordUserMessage(key, "What about the visual style?")
	cb.RecordResponse(key, "2D Artist", "2d-artist", "For tower defense, use chunky sprites...")

	hist := cb.History(key)
	if len(hist) != 1 {
		t.Fatalf("expected 1 completed round, got %d", len(hist))
	}
	if hist[0].UserMessage != "What should the card system look like?" {
		t.Errorf("unexpected user message: %s", hist[0].UserMessage)
	}
	if len(hist[0].Responses) != 2 {
		t.Errorf("expected 2 responses, got %d", len(hist[0].Responses))
	}
}

func TestConvoBuffer_FIFOEviction(t *testing.T) {
	cb := NewConvoBuffer()
	key := "test"

	for i := 0; i < 12; i++ {
		cb.RecordUserMessage(key, strings.Repeat("msg", i+1))
		cb.RecordResponse(key, "Agent", "", "reply")
	}

	hist := cb.History(key)
	if len(hist) != maxHistoryRounds {
		t.Fatalf("expected max %d rounds, got %d", maxHistoryRounds, len(hist))
	}
	// Most recent first — round 11 (0-indexed: i=10, text="msg" * 11)
	if hist[0].UserMessage != strings.Repeat("msg", 11) {
		t.Errorf("expected round 11 as most recent, got %q", hist[0].UserMessage)
	}
}

func TestConvoBuffer_EmptyHistory(t *testing.T) {
	cb := NewConvoBuffer()
	hist := cb.History("nonexistent")
	if hist != nil {
		t.Errorf("expected nil for empty history, got %v", hist)
	}
}

func TestConvoBuffer_FormatHistory(t *testing.T) {
	cb := NewConvoBuffer()
	key := "prism"

	cb.RecordUserMessage(key, "First question")
	cb.RecordResponse(key, "Agent", "", "First answer")
	cb.RecordUserMessage(key, "Second question")
	cb.RecordResponse(key, "Agent", "", "Second answer")
	cb.RecordUserMessage(key, "Third question")

	hist := cb.History(key)
	formatted := FormatHistory(hist)

	if !strings.Contains(formatted, "## Recent conversation") {
		t.Error("missing header")
	}
	if !strings.Contains(formatted, "Round 2 (latest)") {
		t.Error("missing latest round marker")
	}
	if !strings.Contains(formatted, "First question") {
		t.Error("missing first round content")
	}
}

func TestConvoBuffer_FormatHistoryTruncation(t *testing.T) {
	cb := NewConvoBuffer()
	key := "test"

	cb.RecordUserMessage(key, "question")
	longText := strings.Repeat("x", 300)
	cb.RecordResponse(key, "Agent", "", longText)
	cb.RecordUserMessage(key, "next")

	hist := cb.History(key)
	formatted := FormatHistory(hist)

	if !strings.Contains(formatted, "...") {
		t.Error("expected truncation indicator")
	}
}

func TestConvoBuffer_FormatHistoryEmpty(t *testing.T) {
	formatted := FormatHistory(nil)
	if formatted != "" {
		t.Errorf("expected empty string, got %q", formatted)
	}
}

func TestConvoBuffer_MultipleChannels(t *testing.T) {
	cb := NewConvoBuffer()

	cb.RecordUserMessage("prism", "prism msg")
	cb.RecordResponse("prism", "Agent", "", "prism reply")
	cb.RecordUserMessage("prism", "prism msg 2")

	cb.RecordUserMessage("slack", "slack msg")
	cb.RecordResponse("slack", "Agent", "", "slack reply")
	cb.RecordUserMessage("slack", "slack msg 2")

	prismHist := cb.History("prism")
	slackHist := cb.History("slack")

	if len(prismHist) != 1 || prismHist[0].UserMessage != "prism msg" {
		t.Error("prism history incorrect")
	}
	if len(slackHist) != 1 || slackHist[0].UserMessage != "slack msg" {
		t.Error("slack history incorrect")
	}
}

// --- Phase 1.5 tests ---

func TestConvoBuffer_Stores10Rounds(t *testing.T) {
	cb := NewConvoBuffer()
	key := "test"

	for i := 0; i < 12; i++ {
		cb.RecordUserMessage(key, strings.Repeat("m", i+1))
		cb.RecordResponse(key, "Agent", "", "reply")
	}
	cb.RecordUserMessage(key, "final")

	hist := cb.History(key)
	if len(hist) != 10 {
		t.Fatalf("expected 10 stored rounds, got %d", len(hist))
	}
}

func TestConvoBuffer_HasGuestSpoken_True(t *testing.T) {
	cb := NewConvoBuffer()
	key := "test"

	cb.RecordUserMessage(key, "hello")
	cb.RecordResponse(key, "Tolkien", "tolkien", "Once upon a time")
	cb.RecordUserMessage(key, "next")

	if !cb.HasGuestSpoken(key, "tolkien") {
		t.Error("expected HasGuestSpoken to return true for tolkien")
	}
}

func TestConvoBuffer_HasGuestSpoken_False(t *testing.T) {
	cb := NewConvoBuffer()
	key := "test"

	cb.RecordUserMessage(key, "hello")
	cb.RecordResponse(key, "Shigeru", "shigeru", "Let me think")
	cb.RecordUserMessage(key, "next")

	if cb.HasGuestSpoken(key, "tolkien") {
		t.Error("expected HasGuestSpoken to return false for tolkien")
	}
}

func TestConvoBuffer_HasGuestSpoken_CurrentRound(t *testing.T) {
	cb := NewConvoBuffer()
	key := "test"

	cb.RecordUserMessage(key, "hello")
	cb.RecordResponse(key, "Tolkien", "tolkien", "working on it")

	if !cb.HasGuestSpoken(key, "tolkien") {
		t.Error("expected HasGuestSpoken to return true for current round response")
	}
}

func TestFormatHistoryN_RespectsMaxRounds(t *testing.T) {
	var rounds []*ConvoRound
	for i := 0; i < 10; i++ {
		rounds = append(rounds, &ConvoRound{
			UserMessage: strings.Repeat("q", i+1),
			Responses:   []ConvoResponse{{Speaker: "Agent", Text: "reply"}},
		})
	}

	formatted := FormatHistoryN(rounds, 3, 200)
	count := strings.Count(formatted, "### Round")
	if count != 3 {
		t.Errorf("expected 3 rounds in output, got %d", count)
	}
}

func TestFormatHistoryN_RespectsExpandedTruncLen(t *testing.T) {
	longText := strings.Repeat("x", 500)
	rounds := []*ConvoRound{
		{
			UserMessage: "question",
			Responses:   []ConvoResponse{{Speaker: "Agent", Text: longText}},
		},
	}

	expanded := FormatHistoryN(rounds, 10, 800)
	if strings.Contains(expanded, "...") {
		t.Error("500-char response should not be truncated with truncLen=800")
	}

	normal := FormatHistoryN(rounds, 3, 200)
	if !strings.Contains(normal, "...") {
		t.Error("500-char response should be truncated with truncLen=200")
	}
}

func TestFormatHistory_BackwardCompatible(t *testing.T) {
	var rounds []*ConvoRound
	for i := 0; i < 5; i++ {
		rounds = append(rounds, &ConvoRound{
			UserMessage: strings.Repeat("q", i+1),
			Responses:   []ConvoResponse{{Speaker: "Agent", Text: strings.Repeat("a", 300)}},
		})
	}

	formatted := FormatHistory(rounds)
	count := strings.Count(formatted, "### Round")
	if count != 3 {
		t.Errorf("FormatHistory should show 3 rounds (backward compat), got %d", count)
	}
	if !strings.Contains(formatted, "...") {
		t.Error("FormatHistory should truncate at 200 chars (backward compat)")
	}
}

func TestFormatHistoryN_EmptyRounds(t *testing.T) {
	result := FormatHistoryN(nil, 10, 800)
	if result != "" {
		t.Errorf("expected empty string for nil rounds, got %q", result)
	}
}

func TestFormatHistoryN_FewerRoundsThanMax(t *testing.T) {
	rounds := []*ConvoRound{
		{UserMessage: "q1", Responses: []ConvoResponse{{Speaker: "A", Text: "r1"}}},
		{UserMessage: "q2", Responses: []ConvoResponse{{Speaker: "A", Text: "r2"}}},
	}
	formatted := FormatHistoryN(rounds, 10, 800)
	count := strings.Count(formatted, "### Round")
	if count != 2 {
		t.Errorf("expected 2 rounds (all available), got %d", count)
	}
}
