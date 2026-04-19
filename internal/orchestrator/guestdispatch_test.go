package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/guests"
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
	prompt := buildGuestPrompt("You are a game designer.", "Game project", "Key decisions...", "## Recent conversation\n...", "What about cards?", GuestPromptOpts{Mode: types.ModeChat})

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
		Mode:        types.ModePlan,
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
	prompt := buildGuestPrompt("You are a designer.", "project", "context", "history", "Do something", GuestPromptOpts{Mode: types.ModeLoop})

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
		GuestPromptOpts{Mode: types.ModeLoop},
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
	prompt := buildGuestPrompt("You are a writer.", "", "", "", "Write a story", GuestPromptOpts{Mode: types.ModeChat})

	if strings.Contains(prompt, "## Project Context") {
		t.Error("should omit project context when empty")
	}
	if strings.Contains(prompt, "## Session Context") {
		t.Error("should omit session context when empty")
	}
}

func TestBuildGuestPrompt_PlanModeWithoutContent(t *testing.T) {
	prompt := buildGuestPrompt("You are a designer.", "", "", "", "Edit plan", GuestPromptOpts{
		Mode:        types.ModePlan,
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
		Mode:        types.ModeLoop,
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
		Mode:        types.ModeLoop,
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
		Mode:        types.ModeLoop,
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

// --- feature context injection tests ---

func TestBuildGuestPrompt_WithFeatureContext(t *testing.T) {
	prompt := buildGuestPrompt("You are a 2D artist.", "Game project", "", "", "Draw a sprite", GuestPromptOpts{
		FeatureContext: "## Feature Context: tower-defense\nPhase: prototyping\n\n### Plan Summary\nBuild towers.",
	})
	if !strings.Contains(prompt, "## Feature Context: tower-defense") {
		t.Error("prompt should contain feature context header")
	}
	if !strings.Contains(prompt, "Phase: prototyping") {
		t.Error("prompt should contain feature phase")
	}
	if !strings.Contains(prompt, "Build towers.") {
		t.Error("prompt should contain plan summary")
	}
}

func TestBuildGuestPrompt_NoFeatureContextWhenEmpty(t *testing.T) {
	prompt := buildGuestPrompt("You are a designer.", "", "", "", "Do something", GuestPromptOpts{
		FeatureContext: "",
	})
	if strings.Contains(prompt, "## Feature Context") {
		t.Error("should not include feature context when empty")
	}
}

// --- resolveGuestTools tests ---

func TestResolveGuestTools(t *testing.T) {
	emptyFP := "sha256:" + fmt.Sprintf("%x", sha256Sum(nil))

	tests := []struct {
		name  string
		agent guests.GuestAgent
		want  []string
	}{
		{
			name:  "AllowedTools takes precedence",
			agent: guests.GuestAgent{AllowedTools: []string{"mcp__unity__*", "WebSearch"}, RequirementsFingerprint: "sha256:abc"},
			want:  []string{"mcp__unity__*", "WebSearch"},
		},
		{
			name:  "falls back to fingerprint check with requirements",
			agent: guests.GuestAgent{RequirementsFingerprint: "sha256:notempty"},
			want:  []string{"WebSearch", "WebFetch"},
		},
		{
			name:  "empty fingerprint returns nil",
			agent: guests.GuestAgent{RequirementsFingerprint: emptyFP},
			want:  nil,
		},
		{
			name:  "no fingerprint returns nil",
			agent: guests.GuestAgent{},
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveGuestTools(tt.agent)
			if len(got) != len(tt.want) {
				t.Fatalf("resolveGuestTools() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("resolveGuestTools()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// --- isToolAllowed tests ---

func TestIsToolAllowed(t *testing.T) {
	tests := []struct {
		name         string
		toolName     string
		allowedTools []string
		want         bool
	}{
		{
			name:         "exact match",
			toolName:     "WebSearch",
			allowedTools: []string{"WebSearch", "WebFetch"},
			want:         true,
		},
		{
			name:         "exact no match",
			toolName:     "Bash",
			allowedTools: []string{"WebSearch", "WebFetch"},
			want:         false,
		},
		{
			name:         "wildcard match mcp",
			toolName:     "mcp__mcp-unity__CreateObject",
			allowedTools: []string{"mcp__mcp-unity__*"},
			want:         true,
		},
		{
			name:         "wildcard match http",
			toolName:     "http__image_gen__generate",
			allowedTools: []string{"http__image_gen__*"},
			want:         true,
		},
		{
			name:         "wildcard no match different prefix",
			toolName:     "mcp__other__Foo",
			allowedTools: []string{"mcp__mcp-unity__*"},
			want:         false,
		},
		{
			name:         "empty allowedTools denies all",
			toolName:     "WebSearch",
			allowedTools: []string{},
			want:         false,
		},
		{
			name:         "nil allowedTools denies all",
			toolName:     "WebSearch",
			allowedTools: nil,
			want:         false,
		},
		{
			name:         "mixed exact and wildcard",
			toolName:     "WebSearch",
			allowedTools: []string{"mcp__unity__*", "WebSearch"},
			want:         true,
		},
		{
			name:         "wildcard star alone matches everything",
			toolName:     "anything",
			allowedTools: []string{"*"},
			want:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isToolAllowed(tt.toolName, tt.allowedTools)
			if got != tt.want {
				t.Errorf("isToolAllowed(%q, %v) = %v, want %v", tt.toolName, tt.allowedTools, got, tt.want)
			}
		})
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

func TestDispatchSelectedGuests_StreamsDeltas(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	agentsDir := dir + "/agents"
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentsDir+"/artist.md", []byte("---\nname: Artist\ndescription: draws things\nrole: artist\n---\nYou are an artist."), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if opts.OnStream != nil {
			opts.OnStream("Hello ")
			opts.OnStream("world")
		}
		return &types.RunResult{Output: "Hello world"}, nil
	}

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	idx := &guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"artist": {ID: "artist", Name: "Artist", Description: "draws things"},
		},
	}

	dispatchSelectedGuests(guestDispatchParams{
		ctx:         context.Background(),
		selectedIDs: []string{"artist"},
		msg: channel.IncomingMessage{
			ChannelKind: "test",
			ThreadID:    "t1",
			Text:        "Draw something",
		},
		guestsDir:  agentsDir,
		runner:     runner,
		sharedCtx:  NewSharedContext(),
		convoBuf:   NewConvoBuffer(),
		channelMgr: mgr,
		logger:     testSuggestLogger(),
		guestIndex: idx,
	})

	sent := ch.sentMessages()
	var deltas []string
	doneCount := 0
	var textMsgs []string
	for _, m := range sent {
		if m.StreamDelta != "" {
			deltas = append(deltas, m.StreamDelta)
			if m.GuestID != "artist" {
				t.Errorf("stream delta GuestID = %q, want artist", m.GuestID)
			}
			if m.ThreadID != "t1" {
				t.Errorf("stream delta ThreadID = %q, want t1", m.ThreadID)
			}
		}
		if m.StreamDone {
			doneCount++
			if m.GuestID != "artist" {
				t.Errorf("stream done GuestID = %q, want artist", m.GuestID)
			}
		}
		if m.Text != "" && !strings.HasPrefix(m.Text, "[") {
			textMsgs = append(textMsgs, m.Text)
		}
	}

	if len(deltas) == 0 {
		t.Fatal("expected at least one stream delta, got none")
	}
	if doneCount != 1 {
		t.Errorf("expected 1 stream done, got %d", doneCount)
	}
	if len(textMsgs) > 0 {
		t.Errorf("expected no duplicate final Text (content was streamed), got %v", textMsgs)
	}
}

func TestDispatchSelectedGuests_StreamDoneOnError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	agentsDir := dir + "/agents"
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentsDir+"/broken.md", []byte("---\nname: Broken\ndescription: fails\nrole: test\n---\nFails."), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return nil, fmt.Errorf("simulated failure")
	}

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	idx := &guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"broken": {ID: "broken", Name: "Broken", Description: "fails"},
		},
	}

	dispatchSelectedGuests(guestDispatchParams{
		ctx:         context.Background(),
		selectedIDs: []string{"broken"},
		msg: channel.IncomingMessage{
			ChannelKind: "test",
			ThreadID:    "t2",
			Text:        "Do something",
		},
		guestsDir:  agentsDir,
		runner:     runner,
		sharedCtx:  NewSharedContext(),
		convoBuf:   NewConvoBuffer(),
		channelMgr: mgr,
		logger:     testSuggestLogger(),
		guestIndex: idx,
	})

	sent := ch.sentMessages()
	doneCount := 0
	for _, m := range sent {
		if m.StreamDone {
			doneCount++
		}
	}
	if doneCount != 1 {
		t.Errorf("expected StreamDone even on error, got %d", doneCount)
	}
}

func buildTestGuestIndex(ids ...string) *guests.GuestIndex {
	agents := make(map[string]guests.GuestAgent, len(ids))
	for _, id := range ids {
		name := strings.ToUpper(id[:1]) + id[1:]
		agents[id] = guests.GuestAgent{
			ID:   id,
			Name: name,
		}
	}
	return &guests.GuestIndex{Agents: agents}
}

func TestDetectInviteIntent_MatchesInactiveGuest(t *testing.T) {
	idx := buildTestGuestIndex("tolkien", "shigeru")
	results := detectInviteIntent("invite tolkien to the chat", nil, idx)
	if len(results) != 1 {
		t.Fatalf("expected 1 match, got %d", len(results))
	}
	if results[0].GuestID != "tolkien" {
		t.Errorf("guest_id = %q, want %q", results[0].GuestID, "tolkien")
	}
	if results[0].Reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestDetectInviteIntent_IgnoresActiveGuest(t *testing.T) {
	idx := buildTestGuestIndex("tolkien", "shigeru")
	results := detectInviteIntent("invite tolkien to the chat", []string{"tolkien"}, idx)
	if results != nil {
		t.Errorf("expected nil for already-active guest, got %+v", results)
	}
}

func TestDetectInviteIntent_NoMatchForUnrelatedMessage(t *testing.T) {
	idx := buildTestGuestIndex("tolkien", "shigeru")
	results := detectInviteIntent("tell me about tolkien's writing style", nil, idx)
	if results != nil {
		t.Errorf("expected nil for message without invite verb, got %+v", results)
	}
}

func TestDetectInviteIntent_CaseInsensitive(t *testing.T) {
	idx := buildTestGuestIndex("tolkien")
	results := detectInviteIntent("INVITE TOLKIEN", nil, idx)
	if len(results) != 1 {
		t.Fatalf("expected 1 match, got %d", len(results))
	}
	if results[0].GuestID != "tolkien" {
		t.Errorf("guest_id = %q, want %q", results[0].GuestID, "tolkien")
	}
}

func TestDetectInviteIntent_MatchesByName(t *testing.T) {
	agents := map[string]guests.GuestAgent{
		"narrative-writer": {ID: "narrative-writer", Name: "Tolkien"},
	}
	idx := &guests.GuestIndex{Agents: agents}
	results := detectInviteIntent("add Tolkien please", nil, idx)
	if len(results) != 1 {
		t.Fatalf("expected 1 match, got %d", len(results))
	}
	if results[0].GuestID != "narrative-writer" {
		t.Errorf("guest_id = %q, want %q", results[0].GuestID, "narrative-writer")
	}
}

func TestDetectInviteIntent_NilIndex(t *testing.T) {
	results := detectInviteIntent("invite tolkien", nil, nil)
	if results != nil {
		t.Errorf("expected nil for nil index, got %+v", results)
	}
}

func TestDetectInviteIntent_AlternateVerbs(t *testing.T) {
	idx := buildTestGuestIndex("tolkien")
	for _, text := range []string{
		"bring in tolkien",
		"pull in tolkien",
		"add tolkien to the chat",
	} {
		results := detectInviteIntent(text, nil, idx)
		if len(results) == 0 {
			t.Errorf("expected match for %q, got nil", text)
		}
	}
}

func TestDetectInviteIntent_MultipleGuests(t *testing.T) {
	idx := buildTestGuestIndex("tolkien", "shigeru", "walt")
	results := detectInviteIntent("add tolkien and walt please", nil, idx)
	if len(results) != 2 {
		t.Fatalf("expected 2 matches, got %d: %+v", len(results), results)
	}
	got := map[string]bool{}
	for _, r := range results {
		got[r.GuestID] = true
	}
	if !got["tolkien"] || !got["walt"] {
		t.Errorf("expected tolkien and walt, got %v", got)
	}
	if got["shigeru"] {
		t.Error("shigeru should not be matched")
	}
}

func TestDetectInviteIntent_MultipleGuests_PartiallyActive(t *testing.T) {
	idx := buildTestGuestIndex("tolkien", "shigeru", "walt")
	results := detectInviteIntent("invite tolkien and walt", []string{"tolkien"}, idx)
	if len(results) != 1 {
		t.Fatalf("expected 1 match (tolkien already active), got %d: %+v", len(results), results)
	}
	if results[0].GuestID != "walt" {
		t.Errorf("guest_id = %q, want %q", results[0].GuestID, "walt")
	}
}

func TestBuildGuestPrompt_ContextReportingInstructions(t *testing.T) {
	prompt := buildGuestPrompt("You are an artist.", "Game", "", "", "Draw sprites", GuestPromptOpts{
		FeatureContext: "## Feature Context: td\nPhase: scene-layout",
	})

	if !strings.Contains(prompt, "## Context Reporting") {
		t.Error("prompt should include Context Reporting section when feature context is present")
	}
	if !strings.Contains(prompt, "context_report") {
		t.Error("prompt should mention context_report format")
	}
	if !strings.Contains(prompt, "sprite") {
		t.Error("prompt should mention canonical metadata keys")
	}
	if !strings.Contains(prompt, "kebab-case") {
		t.Error("prompt should mention stable ID requirement")
	}
}

func TestBuildGuestPrompt_NoContextReportingWithoutFeature(t *testing.T) {
	prompt := buildGuestPrompt("You are a designer.", "", "", "", "Hello", GuestPromptOpts{})

	if strings.Contains(prompt, "## Context Reporting") {
		t.Error("should not include Context Reporting when no feature context")
	}
}

func TestBuildGuestPrompt_UserContextInjected(t *testing.T) {
	prompt := buildGuestPrompt("You are a designer.", "Project", "", "", "Hello", GuestPromptOpts{
		UserContext: "Prefers visual explanations\nLikes dark mode",
	})

	if !strings.Contains(prompt, "## User Context") {
		t.Error("prompt should contain ## User Context header")
	}
	if !strings.Contains(prompt, "Prefers visual explanations") {
		t.Error("prompt should contain user context content")
	}
}

func TestBuildGuestPrompt_UserContextEmpty(t *testing.T) {
	prompt := buildGuestPrompt("You are a designer.", "Project", "", "", "Hello", GuestPromptOpts{
		UserContext: "",
	})

	if strings.Contains(prompt, "## User Context") {
		t.Error("prompt should NOT contain ## User Context when empty")
	}
}

func TestBuildGuestPrompt_UserContextOrdering(t *testing.T) {
	prompt := buildGuestPrompt("You are a designer.", "Project summary", "", "", "Hello", GuestPromptOpts{
		UserContext: "USER_CONTEXT_MARKER",
	})

	commitIdx := strings.Index(prompt, "## Character Commitment")
	userCtxIdx := strings.Index(prompt, "## User Context")
	projectIdx := strings.Index(prompt, "## Project Context")

	if commitIdx < 0 || userCtxIdx < 0 || projectIdx < 0 {
		t.Fatal("one or more expected sections not found")
	}
	if commitIdx >= userCtxIdx {
		t.Error("Character Commitment should appear before User Context")
	}
	if userCtxIdx >= projectIdx {
		t.Error("User Context should appear before Project Context")
	}
}

// TestBuildGuestPrompt_PlanModeToolPolicy asserts L2's prompt directive:
// a Plan-mode guest invocation must include the explicit "Plan Mode Tool
// Policy" section that tells the agent not to call tools. This is the
// prompt-layer half of the tool strip; RunOpts.ToolsDisabled is the
// harness-layer half and is asserted separately below.
func TestBuildGuestPrompt_PlanModeToolPolicy(t *testing.T) {
	prompt := buildGuestPrompt("You are a designer.", "", "", "", "Draft the level", GuestPromptOpts{
		Mode:        types.ModePlan,
		PlanContent: "# My Plan\n\nTBD",
	})
	if !strings.Contains(prompt, "## Plan Mode Tool Policy") {
		t.Error("prompt should include Plan Mode Tool Policy section in Plan mode")
	}
	if !strings.Contains(prompt, "You MUST NOT call any tools this turn") {
		t.Error("prompt should explicitly forbid tool calls in Plan mode")
	}
	if !strings.Contains(prompt, "plan-edit") {
		t.Error("prompt should tell the agent to use plan-edit blocks for edits")
	}

	// Chat mode must NOT carry this directive.
	chatPrompt := buildGuestPrompt("You are a designer.", "", "", "", "Draft", GuestPromptOpts{
		Mode: types.ModeChat,
	})
	if strings.Contains(chatPrompt, "## Plan Mode Tool Policy") {
		t.Error("Chat-mode prompt should NOT include Plan Mode Tool Policy section")
	}
}

// TestDispatchSelectedGuests_PlanModeStripsTools asserts L2's harness
// directive: every run in Plan mode must receive RunOpts.ToolsDisabled=true
// and an empty AllowedTools slice, regardless of what the guest's
// frontmatter declares. This is the belt-and-suspenders guarantee that
// prevents a future refactor from accidentally re-enabling tools for
// plan-time contributions.
func TestDispatchSelectedGuests_PlanModeStripsTools(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	agentsDir := dir + "/agents"
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Frontmatter deliberately declares an allowed_tools list to prove
	// that Plan mode overrides it.
	body := "---\nname: Artist\ndescription: draws things\nrole: artist\nallowed_tools:\n  - Edit\n  - Write\n---\nYou are an artist."
	if err := os.WriteFile(agentsDir+"/artist.md", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var observed types.RunOpts
	var observedOnce sync.Once
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		observedOnce.Do(func() { observed = opts })
		return &types.RunResult{Output: "ok"}, nil
	}

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	idx := &guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"artist": {
				ID:           "artist",
				Name:         "Artist",
				Description:  "draws things",
				AllowedTools: []string{"Edit", "Write"},
			},
		},
	}

	dispatchSelectedGuests(guestDispatchParams{
		ctx:         context.Background(),
		selectedIDs: []string{"artist"},
		msg: channel.IncomingMessage{
			ChannelKind: "test",
			ThreadID:    "t-plan",
			Text:        "[system:plan] contribute to the plan",
		},
		guestsDir:  agentsDir,
		runner:     runner,
		sharedCtx:  NewSharedContext(),
		convoBuf:   NewConvoBuffer(),
		channelMgr: mgr,
		logger:     testSuggestLogger(),
		guestIndex: idx,
		mode:       types.ModePlan,
	})

	if !observed.ToolsDisabled {
		t.Error("Plan-mode RunOpts.ToolsDisabled = false, want true")
	}
	if len(observed.AllowedTools) != 0 {
		t.Errorf("Plan-mode AllowedTools = %v, want empty slice", observed.AllowedTools)
	}
}

// TestDispatchSelectedGuests_PerGuestMaxTurns pins the per-agent
// max_turns hand-off: two guests dispatched in the same round must
// receive distinct MaxTurns values taken from their frontmatter.
// Before this change, maxTurns was computed once per round from the
// merged mcp-active flag, so every guest in a round shared the same
// cap regardless of what their agent.md declared.
func TestDispatchSelectedGuests_PerGuestMaxTurns(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	agentsDir := dir + "/agents"
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fastBody := "---\nname: Fast\ndescription: one-shot\nrole: artist\nmax_turns: 4\n---\nFast executor."
	if err := os.WriteFile(agentsDir+"/fast.md", []byte(fastBody), 0o644); err != nil {
		t.Fatal(err)
	}
	slowBody := "---\nname: Slow\ndescription: iterative\nrole: designer\nmax_turns: 20\n---\nSlow author."
	if err := os.WriteFile(agentsDir+"/slow.md", []byte(slowBody), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	observed := make(map[string]int)
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		mu.Lock()
		if strings.Contains(opts.Prompt, "Fast executor.") {
			observed["fast"] = opts.MaxTurns
		} else if strings.Contains(opts.Prompt, "Slow author.") {
			observed["slow"] = opts.MaxTurns
		}
		mu.Unlock()
		return &types.RunResult{Output: "ok"}, nil
	}

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	idx := &guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"fast": {ID: "fast", Name: "Fast", Description: "one-shot", MaxTurns: 4},
			"slow": {ID: "slow", Name: "Slow", Description: "iterative", MaxTurns: 20},
		},
	}

	dispatchSelectedGuests(guestDispatchParams{
		ctx:         context.Background(),
		selectedIDs: []string{"fast", "slow"},
		msg: channel.IncomingMessage{
			ChannelKind: "test",
			ThreadID:    "t-per-guest",
			Text:        "Work on your tasks.",
		},
		guestsDir:        agentsDir,
		runner:           runner,
		sharedCtx:        NewSharedContext(),
		convoBuf:         NewConvoBuffer(),
		channelMgr:       mgr,
		logger:           testSuggestLogger(),
		guestIndex:       idx,
		guestMCPMaxTurns: 32,
	})

	mu.Lock()
	defer mu.Unlock()
	if observed["fast"] != 4 {
		t.Errorf("fast MaxTurns = %d, want 4 (per-agent override)", observed["fast"])
	}
	if observed["slow"] != 20 {
		t.Errorf("slow MaxTurns = %d, want 20 (per-agent override)", observed["slow"])
	}
}
