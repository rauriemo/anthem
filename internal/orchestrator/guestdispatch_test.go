package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/rauriemo/anthem/internal/types"
)

func TestExtractPlanEdit_WithBlock(t *testing.T) {
	response := "I think section 2 needs work.\n\n```plan-edit\n## Section 2\n\nUpdated content here.\n```\n\nLet me know what you think."
	explanation, planMd, hasEdit := extractPlanEdit(response)

	if !hasEdit {
		t.Fatal("expected hasEdit to be true")
	}
	if planMd != "## Section 2\n\nUpdated content here." {
		t.Errorf("unexpected plan markdown: %q", planMd)
	}
	if explanation == "" {
		t.Error("expected non-empty explanation")
	}
}

func TestExtractPlanEdit_NoBlock(t *testing.T) {
	response := "I think this looks good. No changes needed."
	explanation, planMd, hasEdit := extractPlanEdit(response)

	if hasEdit {
		t.Error("expected hasEdit to be false")
	}
	if planMd != "" {
		t.Errorf("expected empty plan markdown, got %q", planMd)
	}
	if explanation != response {
		t.Errorf("expected full response as explanation, got %q", explanation)
	}
}

func TestExtractPlanEdit_NestedCodeBlocks(t *testing.T) {
	response := "Here is my edit:\n\n```plan-edit\n## Code Section\n\nSome code:\n\n    func main() {}\n\n```\n\nDone."
	_, planMd, hasEdit := extractPlanEdit(response)

	if !hasEdit {
		t.Fatal("expected hasEdit to be true")
	}
	if planMd == "" {
		t.Error("expected non-empty plan markdown")
	}
}

func TestBuildGuestPrompt_FastMode(t *testing.T) {
	prompt := buildGuestPrompt("You are a game designer.", "Game project", "Key decisions...", "## Recent conversation\n...", "What about cards?", GuestPromptOpts{Mode: "fast"})

	if len(prompt) == 0 {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(prompt, "Be concise.") {
		t.Error("fast mode should include 'Be concise.'")
	}
	if !strings.Contains(prompt, "game designer") {
		t.Error("should include persona")
	}
}

func TestBuildGuestPrompt_PlanMode(t *testing.T) {
	prompt := buildGuestPrompt("You are a game designer.", "", "", "", "Edit the plan", GuestPromptOpts{
		Mode:        "plan",
		PlanContent: "# My Plan\n\n## Section 1\nContent",
	})

	if !strings.Contains(prompt, "## Current Plan") {
		t.Error("plan mode should include current plan")
	}
	if !strings.Contains(prompt, "plan-edit") {
		t.Error("plan mode should include edit instructions")
	}
}

func TestBuildGuestPrompt_AgentMode(t *testing.T) {
	prompt := buildGuestPrompt("You are a designer.", "project", "context", "history", "Do something", GuestPromptOpts{Mode: "agent"})

	if strings.Contains(prompt, "Be concise.") {
		t.Error("agent mode should not include 'Be concise.'")
	}
	if strings.Contains(prompt, "## Current Plan") {
		t.Error("agent mode without plan should not include plan section")
	}
}

func TestBuildGuestPrompt_IncludesAllSections(t *testing.T) {
	prompt := buildGuestPrompt(
		"You are a 2D artist.",
		"Tower defense game",
		"Decided on pixel art style",
		"## Recent conversation\nUser: hello\nArtist: hi",
		"Draw a tower",
		GuestPromptOpts{Mode: "agent"},
	)

	if !strings.Contains(prompt, "## Project Context") {
		t.Error("should include project context section")
	}
	if !strings.Contains(prompt, "## Session Context") {
		t.Error("should include session context section")
	}
	if !strings.Contains(prompt, "## User Message") {
		t.Error("should include user message section")
	}
	if !strings.Contains(prompt, "Tower defense game") {
		t.Error("should include project summary content")
	}
	if !strings.Contains(prompt, "Decided on pixel art style") {
		t.Error("should include shared context content")
	}
}

func TestBuildGuestPrompt_OmitsEmptySections(t *testing.T) {
	prompt := buildGuestPrompt("You are a writer.", "", "", "", "Write a story", GuestPromptOpts{Mode: "fast"})

	if strings.Contains(prompt, "## Project Context") {
		t.Error("should omit project context when empty")
	}
	if strings.Contains(prompt, "## Session Context") {
		t.Error("should omit session context when empty")
	}
}

func TestBuildGuestPrompt_PlanModeWithoutContent(t *testing.T) {
	prompt := buildGuestPrompt("You are a designer.", "", "", "", "Edit plan", GuestPromptOpts{
		Mode:        "plan",
		PlanContent: "",
	})

	if strings.Contains(prompt, "## Current Plan") {
		t.Error("plan mode without content should not include plan section")
	}
}

func TestExtractJSON_Valid(t *testing.T) {
	input := `Some text before {"guests": ["a", "b"], "context_update": "test"} and after`
	result := extractJSON(input)
	if result != `{"guests": ["a", "b"], "context_update": "test"}` {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestExtractJSON_NoJSON(t *testing.T) {
	input := "no json here"
	result := extractJSON(input)
	if result != input {
		t.Errorf("expected original string, got %q", result)
	}
}

func TestExtractJSON_NestedBraces(t *testing.T) {
	input := `{"outer": {"inner": "value"}}`
	result := extractJSON(input)
	if result != input {
		t.Errorf("expected full nested JSON, got %q", result)
	}
}

func TestExtractJSON_UnclosedBrace(t *testing.T) {
	input := `prefix {"guests": ["a"]`
	result := extractJSON(input)
	if result != `{"guests": ["a"]` {
		t.Errorf("expected partial JSON from first brace, got %q", result)
	}
}

func TestFallbackAllGuests(t *testing.T) {
	summaries := []GuestSummary{
		{ID: "a", Name: "A"},
		{ID: "b", Name: "B"},
	}
	result := fallbackAllGuests(summaries)
	if len(result.Guests) != 2 {
		t.Errorf("expected 2 guests, got %d", len(result.Guests))
	}
	if result.Guests[0] != "a" || result.Guests[1] != "b" {
		t.Errorf("unexpected guest IDs: %v", result.Guests)
	}
}

func TestFallbackAllGuests_Empty(t *testing.T) {
	result := fallbackAllGuests(nil)
	if len(result.Guests) != 0 {
		t.Errorf("expected 0 guests, got %d", len(result.Guests))
	}
}

func TestFallbackAllGuests_OrchestratorExcluded(t *testing.T) {
	summaries := []GuestSummary{{ID: "a", Name: "A"}}
	result := fallbackAllGuests(summaries)
	if result.IncludeOrchestrator {
		t.Error("fallback should default IncludeOrchestrator to false")
	}
}

func TestRoutingResult_ParsesIncludeOrchestrator(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		wantOrch bool
	}{
		{
			name:     "orchestrator included",
			json:     `{"guests": ["a"], "include_orchestrator": true, "context_update": ""}`,
			wantOrch: true,
		},
		{
			name:     "orchestrator excluded",
			json:     `{"guests": ["a"], "include_orchestrator": false, "context_update": ""}`,
			wantOrch: false,
		},
		{
			name:     "field omitted defaults to false",
			json:     `{"guests": ["a"], "context_update": ""}`,
			wantOrch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result RoutingResult
			if err := json.Unmarshal([]byte(tt.json), &result); err != nil {
				t.Fatalf("failed to parse JSON: %v", err)
			}
			if result.IncludeOrchestrator != tt.wantOrch {
				t.Errorf("IncludeOrchestrator = %v, want %v", result.IncludeOrchestrator, tt.wantOrch)
			}
		})
	}
}

func TestBuildGuestPrompt_StoryContext(t *testing.T) {
	sc := &StoryContext{
		Config: "game: TestGame\ngenre: fantasy\n",
		Files: map[string]string{
			"narrative.md": "<!-- id: prologue -->\n## Prologue\n\nOnce upon a time.",
		},
		Sections: map[string]map[string]string{
			"narrative.md": {"prologue": "a1b2c3d4"},
		},
	}

	prompt := buildGuestPrompt("You are a writer.", "", "", "", "Write chapter 2", GuestPromptOpts{
		StoryContext: sc,
	})

	if !strings.Contains(prompt, "## Story Bible") {
		t.Error("should include story bible section")
	}
	if !strings.Contains(prompt, "TestGame") {
		t.Error("should include context.yaml content")
	}
	if !strings.Contains(prompt, "prologue") {
		t.Error("should include section hashes")
	}
	if !strings.Contains(prompt, "a1b2c3d4") {
		t.Error("should include hash values")
	}
	if !strings.Contains(prompt, "story-edit") {
		t.Error("should include story-edit instructions")
	}
	if !strings.Contains(prompt, "PROPOSALS") {
		t.Error("should mention proposals")
	}
}

func TestBuildGuestPrompt_NoStoryContext(t *testing.T) {
	prompt := buildGuestPrompt("You are a designer.", "", "", "", "Balance towers", GuestPromptOpts{
		StoryContext: nil,
	})

	if strings.Contains(prompt, "## Story Bible") {
		t.Error("should not include story bible when StoryContext is nil")
	}
	if strings.Contains(prompt, "story-edit") {
		t.Error("should not include story-edit instructions for non-writer")
	}
}

func TestBuildGuestPrompt_PrismDisplayInstructions(t *testing.T) {
	prompt := buildGuestPrompt("You are a writer.", "Project", "", "", "Show me the docs", GuestPromptOpts{
		Mode:        "agent",
		ChannelKind: "prism",
	})

	if !strings.Contains(prompt, "## Visual Output") {
		t.Error("prism channel should include Visual Output section")
	}
	if !strings.Contains(prompt, "```html") {
		t.Error("prism channel should include html block instructions")
	}
	if !strings.Contains(prompt, "Self-contained HTML") {
		t.Error("prism channel should describe HTML requirements")
	}
}

func TestBuildGuestPrompt_NoPrismDisplayForOtherChannels(t *testing.T) {
	prompt := buildGuestPrompt("You are a writer.", "Project", "", "", "Show me the docs", GuestPromptOpts{
		Mode:        "agent",
		ChannelKind: "",
	})

	if strings.Contains(prompt, "## Visual Output") {
		t.Error("non-prism channel should not include Visual Output section")
	}
	if strings.Contains(prompt, "prism-display") {
		t.Error("non-prism channel should not include prism-display instructions")
	}
}

func TestBuildGuestPrompt_CharacterCommitment(t *testing.T) {
	prompt := buildGuestPrompt("You are a game designer.", "", "", "", "Hello", GuestPromptOpts{})

	if !strings.Contains(prompt, "## Character Commitment") {
		t.Error("prompt should include Character Commitment section")
	}
	if !strings.Contains(prompt, "Never break character") {
		t.Error("prompt should instruct never to break character")
	}
	if !strings.Contains(prompt, "Never say you are an AI") {
		t.Error("prompt should instruct not to disclaim AI identity")
	}
}

func TestBuildGuestPrompt_NoPrismDisplayForCLI(t *testing.T) {
	prompt := buildGuestPrompt("You are a writer.", "Project", "", "", "Show me the docs", GuestPromptOpts{
		Mode:        "agent",
		ChannelKind: "cli",
	})

	if strings.Contains(prompt, "## Visual Output") {
		t.Error("cli channel should not include Visual Output section")
	}
}

func TestExtractLeanDisplayBlocks_GuestPrismDisplay(t *testing.T) {
	response := "Here is the overview.\n\n```prism-display\n{\"kind\":\"html\",\"content\":\"<div><h1>Story Overview</h1></div>\"}\n```\n\nLet me know if you need more."
	cleanText, displays := extractLeanDisplayBlocks(response)

	if len(displays) != 1 {
		t.Fatalf("expected 1 display block, got %d", len(displays))
	}
	if displays[0]["kind"] != "html" {
		t.Errorf("expected kind=html, got %v", displays[0]["kind"])
	}
	if displays[0]["content"] != "<div><h1>Story Overview</h1></div>" {
		t.Errorf("unexpected content: %v", displays[0]["content"])
	}
	if strings.Contains(cleanText, "prism-display") {
		t.Error("cleaned text should not contain prism-display block")
	}
	if !strings.Contains(cleanText, "Here is the overview") {
		t.Error("cleaned text should preserve text outside display blocks")
	}
	if !strings.Contains(cleanText, "Let me know") {
		t.Error("cleaned text should preserve trailing text")
	}
}

func TestExtractLeanDisplayBlocks_GuestNoPrismDisplay(t *testing.T) {
	response := "Just a plain text response with no display blocks."
	cleanText, displays := extractLeanDisplayBlocks(response)

	if len(displays) != 0 {
		t.Errorf("expected no display blocks, got %d", len(displays))
	}
	if strings.TrimRight(cleanText, "\n") != response {
		t.Errorf("clean text should be unchanged, got %q", cleanText)
	}
}

// --- Phase 1 tests ---

func TestRoutingResult_ParsesDirectedText(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantLen int
		wantKey string
		wantVal string
		wantNil bool
	}{
		{
			name:    "directed_text present",
			json:    `{"guests": ["shigeru"], "directed_text": {"shigeru": "evaluate the game"}, "include_orchestrator": false, "context_update": ""}`,
			wantLen: 1,
			wantKey: "shigeru",
			wantVal: "evaluate the game",
		},
		{
			name:    "directed_text omitted",
			json:    `{"guests": ["a"], "include_orchestrator": false, "context_update": ""}`,
			wantNil: true,
		},
		{
			name:    "directed_text empty map",
			json:    `{"guests": ["a"], "directed_text": {}, "include_orchestrator": false, "context_update": ""}`,
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result RoutingResult
			if err := json.Unmarshal([]byte(tt.json), &result); err != nil {
				t.Fatalf("failed to parse JSON: %v", err)
			}
			if tt.wantNil {
				if result.DirectedText != nil {
					t.Errorf("expected nil DirectedText, got %v", result.DirectedText)
				}
				return
			}
			if len(result.DirectedText) != tt.wantLen {
				t.Errorf("expected %d entries, got %d", tt.wantLen, len(result.DirectedText))
			}
			if tt.wantKey != "" {
				if got := result.DirectedText[tt.wantKey]; got != tt.wantVal {
					t.Errorf("DirectedText[%q] = %q, want %q", tt.wantKey, got, tt.wantVal)
				}
			}
		})
	}
}

func TestBuildGuestPrompt_WithFocusText(t *testing.T) {
	prompt := buildGuestPrompt("You are a game designer.", "", "", "", "Do everything", GuestPromptOpts{
		FocusText: "evaluate the card system",
	})
	if !strings.Contains(prompt, "## Your Focus") {
		t.Error("expected ## Your Focus section when FocusText is set")
	}
	if !strings.Contains(prompt, "evaluate the card system") {
		t.Error("expected focus text content in prompt")
	}
	if !strings.Contains(prompt, "## User Message") {
		t.Error("expected user message section even with focus text")
	}
	if !strings.Contains(prompt, "Do everything") {
		t.Error("full user message should still be present")
	}
}

func TestBuildGuestPrompt_NoFocusTextWhenEmpty(t *testing.T) {
	prompt := buildGuestPrompt("You are a designer.", "", "", "", "Do something", GuestPromptOpts{
		FocusText: "",
	})
	if strings.Contains(prompt, "## Your Focus") {
		t.Error("should not include ## Your Focus when FocusText is empty")
	}
}

func TestFallbackAllGuests_DirectedTextNil(t *testing.T) {
	result := fallbackAllGuests([]GuestSummary{{ID: "a", Name: "A"}})
	if result.DirectedText != nil {
		t.Errorf("fallback should have nil DirectedText, got %v", result.DirectedText)
	}
}

// --- Phase 2 tests ---

func TestSuggestGuestToInvite_NoSuggestion(t *testing.T) {
	mockRunner := &mockSuggestRunner{output: `{"guest_id": "", "reason": ""}`}
	result := suggestGuestToInvite(
		context.Background(),
		mockRunner,
		[]GuestSummary{{ID: "tolkien", Name: "Tolkien", Description: "Writer"}},
		"## Recent conversation\nUser: hello\n",
		testSuggestLogger(),
	)
	if result != nil {
		t.Errorf("expected nil result for empty suggestion, got %+v", result)
	}
}

func TestSuggestGuestToInvite_ReturnsSuggestion(t *testing.T) {
	mockRunner := &mockSuggestRunner{output: `{"guest_id": "tolkien", "reason": "Story alignment needed"}`}
	result := suggestGuestToInvite(
		context.Background(),
		mockRunner,
		[]GuestSummary{{ID: "tolkien", Name: "Tolkien", Description: "Writer"}},
		"## Recent conversation\nUser: We need narrative direction\n",
		testSuggestLogger(),
	)
	if result == nil {
		t.Fatal("expected non-nil suggestion")
	}
	if result.GuestID != "tolkien" {
		t.Errorf("expected tolkien, got %q", result.GuestID)
	}
	if result.Reason != "Story alignment needed" {
		t.Errorf("unexpected reason: %q", result.Reason)
	}
}

func TestSuggestGuestToInvite_FailsSilently(t *testing.T) {
	mockRunner := &mockSuggestRunner{err: fmt.Errorf("network error")}
	result := suggestGuestToInvite(
		context.Background(),
		mockRunner,
		[]GuestSummary{{ID: "a", Name: "A", Description: ""}},
		"",
		testSuggestLogger(),
	)
	if result != nil {
		t.Errorf("expected nil on error, got %+v", result)
	}
}

func TestSuggestGuestToInvite_InvalidJSON(t *testing.T) {
	mockRunner := &mockSuggestRunner{output: "not valid json at all"}
	result := suggestGuestToInvite(
		context.Background(),
		mockRunner,
		[]GuestSummary{{ID: "a", Name: "A", Description: ""}},
		"",
		testSuggestLogger(),
	)
	if result != nil {
		t.Errorf("expected nil on invalid JSON, got %+v", result)
	}
}

func TestSuggestGuestToInvite_PromptListsCandidates(t *testing.T) {
	var capturedPrompt string
	mockRunner := &mockSuggestRunner{
		output:        `{"guest_id": "", "reason": ""}`,
		capturePrompt: &capturedPrompt,
	}
	candidates := []GuestSummary{
		{ID: "tolkien", Name: "Tolkien", Description: "World builder"},
		{ID: "shigeru", Name: "Shigeru", Description: "Game designer"},
	}
	suggestGuestToInvite(
		context.Background(),
		mockRunner,
		candidates,
		"",
		testSuggestLogger(),
	)
	if !strings.Contains(capturedPrompt, "tolkien") || !strings.Contains(capturedPrompt, "shigeru") {
		t.Errorf("prompt should list all candidates, got: %s", capturedPrompt[:min(len(capturedPrompt), 200)])
	}
}

type mockSuggestRunner struct {
	output        string
	err           error
	capturePrompt *string
}

func (m *mockSuggestRunner) Run(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
	if m.capturePrompt != nil {
		*m.capturePrompt = opts.Prompt
	}
	if m.err != nil {
		return nil, m.err
	}
	return &types.RunResult{Output: m.output}, nil
}

func (m *mockSuggestRunner) Continue(_ context.Context, _ string, _ string, _ types.ContinueOpts) (*types.RunResult, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockSuggestRunner) Kill(_ int) error {
	return nil
}

func testSuggestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
