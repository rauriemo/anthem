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

	for i := 0; i < 5; i++ {
		cb.RecordUserMessage(key, strings.Repeat("msg", i+1))
		cb.RecordResponse(key, "Agent", "", "reply")
	}

	hist := cb.History(key)
	if len(hist) != 3 {
		t.Fatalf("expected max 3 rounds, got %d", len(hist))
	}
	// Most recent first
	if hist[0].UserMessage != "msgmsgmsgmsg" {
		t.Errorf("expected round 4 as most recent, got %q", hist[0].UserMessage)
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
