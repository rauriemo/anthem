package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/plans"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/workspace"
)

func newPlanTestOrch(t *testing.T, orchRunner *agent.MockRunner) (*Orchestrator, *testChannel) {
	t.Helper()
	tasks := []types.Task{
		{ID: "1", Title: "Existing Task", Status: types.StatusQueued, Labels: []string{"todo"}, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)
	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchAgent := NewOrchestratorAgent(orchRunner, "", "", 100000, 10, 25, 10, 5, testLogger())

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "owner/repo"
	cfg.Tracker.Labels.Active = []string{"todo"}

	home := t.TempDir()
	store, err := plans.NewStore(home)
	if err != nil {
		t.Fatal(err)
	}

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "{{.issue.title}}",
		Tracker:        trk,
		Runner:         agent.NewMockRunner(),
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		OrchAgent:      orchAgent,
		ChannelManager: mgr,
	})
	orch.planStore = store
	return orch, ch
}

func TestHandlePlanMessage_RoutesCorrectly(t *testing.T) {
	planOutput := "Here's a plan:\n```anthem-plan\n# My Plan\n\n## Tasks\n\n### 1. Do X\n- **Labels:** area:backend\n```\n"

	orchRunner := agent.NewMockRunner()
	callCount := 0
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		callCount++
		if strings.Contains(opts.Prompt, "Scout Mode") {
			// Scout phase: return empty explores to trigger fallback
			return &types.RunResult{
				SessionID: "scout-s1",
				Output:    `{"reasoning": "simple request", "explores": [], "user_message": ""}`,
				TokensIn:  50, TokensOut: 20,
			}, nil
		}
		if !strings.Contains(opts.Prompt, "PLANNING mode") {
			t.Error("expected plan mode prompt suffix on fallback")
		}
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    planOutput,
			TokensIn:  100, TokensOut: 50,
		}, nil
	}

	orch, ch := newPlanTestOrch(t, orchRunner)

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] Build a login page",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()

	// Should have: ack, stream-done, display(plan)
	var planDisplay bool
	for _, msg := range sent {
		if msg.Display != nil {
			if dm, ok := msg.Display.(map[string]any); ok {
				if kind, ok := dm["kind"].(string); ok && kind == "plan" {
					planDisplay = true
					content, _ := dm["content"].(string)
					if !strings.Contains(content, "# My Plan") {
						t.Errorf("plan content missing expected heading, got %q", content)
					}
				}
			}
		}
	}
	if !planDisplay {
		t.Error("expected a plan display artifact to be sent")
	}
}

func TestHandlePlanMessage_SavesPlanFile(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if strings.Contains(opts.Prompt, "Scout Mode") {
			return &types.RunResult{
				SessionID: "scout-s1",
				Output:    `{"reasoning": "simple", "explores": [], "user_message": ""}`,
				TokensIn:  10, TokensOut: 5,
			}, nil
		}
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    "```anthem-plan\n# Feature X\n\n## Tasks\n\n### 1. Task A\n```",
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	orch, _ := newPlanTestOrch(t, orchRunner)

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] Create feature X",
		Timestamp:   time.Now(),
	})

	metas, err := orch.planStore.List("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Fatalf("expected 1 plan saved, got %d", len(metas))
	}
	if metas[0].Status != plans.StatusDraft {
		t.Errorf("plan status = %q, want draft", metas[0].Status)
	}
}

func TestExtractPlanBlock(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "standard block",
			input: "Here's a plan:\n```anthem-plan\n# My Plan\n\n## Tasks\n```\nDone.",
			want:  "# My Plan\n\n## Tasks",
		},
		{
			name:  "no block returns empty",
			input: "Just some text without a plan block.",
			want:  "",
		},
		{
			name:  "block at end without closing",
			input: "```anthem-plan\n# Unterminated Plan",
			want:  "# Unterminated Plan",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPlanBlock(tt.input)
			if got != tt.want {
				t.Errorf("extractPlanBlock() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractPlanTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"# Chat Mode Selector\n\n## Tasks\n", "Chat Mode Selector"},
		{"No heading here", ""},
		{"## Not H1\n# Actual Title\n", "Actual Title"},
	}

	for _, tt := range tests {
		got := extractPlanTitle(tt.input)
		if got != tt.want {
			t.Errorf("extractPlanTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractPlanOverview(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "paragraph after title",
			input: "# My Plan\n\nThis is the overview paragraph.\n\n## Tasks\n",
			want:  "This is the overview paragraph.",
		},
		{
			name:  "multi-line paragraph",
			input: "# My Plan\n\nFirst line\nsecond line\n\n## Tasks\n",
			want:  "First line second line",
		},
		{
			name:  "no paragraph",
			input: "# My Plan\n\n## Tasks\n",
			want:  "",
		},
		{
			name:  "no heading",
			input: "Just some text\n",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPlanOverview(tt.input)
			if got != tt.want {
				t.Errorf("extractPlanOverview() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractPlanTasks(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "numbered headings with dots",
			input: "## Tasks\n\n### 1. Setup project\n\n### 2. Add tests\n\n### 3. Deploy\n",
			want:  []string{"Setup project", "Add tests", "Deploy"},
		},
		{
			name:  "numbered headings with parens",
			input: "### 1) First thing\n### 2) Second thing\n",
			want:  []string{"First thing", "Second thing"},
		},
		{
			name:  "no task headings",
			input: "# Plan\n\n## Overview\n\nSome text.\n",
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPlanTasks(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("extractPlanTasks() returned %d tasks, want %d: %v", len(got), len(tt.want), got)
			}
			for i, task := range got {
				if task != tt.want[i] {
					t.Errorf("task[%d] = %q, want %q", i, task, tt.want[i])
				}
			}
		})
	}
}

func TestBuildPlanCard(t *testing.T) {
	content := "# My Plan\n\nThis is the overview.\n\n## Tasks\n\n### 1. Do X\n### 2. Do Y\n"
	card := buildPlanCard(content, "/plans/test.md")

	if !strings.HasPrefix(card, "[plan-card]") {
		t.Errorf("expected [plan-card] prefix, got %q", card[:30])
	}
	if !strings.HasSuffix(card, "[/plan-card]") {
		t.Errorf("expected [/plan-card] suffix, got %q", card[len(card)-30:])
	}
	if !strings.Contains(card, `"title":"My Plan"`) {
		t.Error("card missing title")
	}
	if !strings.Contains(card, `"overview":"This is the overview."`) {
		t.Error("card missing overview")
	}
	if !strings.Contains(card, `"Do X"`) || !strings.Contains(card, `"Do Y"`) {
		t.Error("card missing tasks")
	}
	if !strings.Contains(card, `"planPath":"/plans/test.md"`) {
		t.Error("card missing planPath")
	}
}

func TestFinalizePlan_SendsPlanCard(t *testing.T) {
	planOutput := "```anthem-plan\n# Coverage Plan\n\nOverview text here.\n\n## Tasks\n\n### 1. Add unit tests\n### 2. Fix linter\n```\n"

	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if strings.Contains(opts.Prompt, "Scout Mode") {
			return &types.RunResult{
				SessionID: "scout-s1",
				Output:    `{"reasoning": "simple", "explores": [], "user_message": ""}`,
				TokensIn:  10, TokensOut: 5,
			}, nil
		}
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    planOutput,
			TokensIn:  100, TokensOut: 50,
		}, nil
	}

	orch, ch := newPlanTestOrch(t, orchRunner)

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] Create coverage plan",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	var foundCard bool
	for _, msg := range sent {
		if strings.Contains(msg.Text, "[plan-card]") && strings.Contains(msg.Text, "[/plan-card]") {
			foundCard = true
			if !strings.Contains(msg.Text, `"title":"Coverage Plan"`) {
				t.Errorf("plan card missing title, got %q", msg.Text)
			}
			if !strings.Contains(msg.Text, `"Add unit tests"`) {
				t.Errorf("plan card missing tasks, got %q", msg.Text)
			}
		}
	}
	if !foundCard {
		t.Error("expected a [plan-card] message to be sent after plan finalization")
	}
}

func TestFinalizePlan_StableDisplayIDPerPlanID(t *testing.T) {
	planOutputV1 := "```anthem-plan\n# My Plan\n\nFirst draft.\n\n## Tasks\n\n### 1. Do X\n```"
	planOutputV2 := "```anthem-plan\n# My Plan\n\nRefined second draft.\n\n## Tasks\n\n### 1. Do X better\n```"

	orchRunner := agent.NewMockRunner()
	callIdx := 0
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if strings.Contains(opts.Prompt, "Scout Mode") {
			return &types.RunResult{
				SessionID: "scout-s",
				Output:    `{"reasoning": "s", "explores": [], "user_message": ""}`,
			}, nil
		}
		callIdx++
		out := planOutputV1
		if callIdx >= 2 {
			out = planOutputV2
		}
		return &types.RunResult{SessionID: "plan-s", Output: out}, nil
	}

	orch, ch := newPlanTestOrch(t, orchRunner)

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] Draft it",
		Timestamp:   time.Now(),
	})

	firstID := planDisplayID(t, ch, "first broadcast")
	ch.reset()

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] Refine it",
		Timestamp:   time.Now(),
	})

	secondID := planDisplayID(t, ch, "second broadcast")

	if firstID != secondID {
		t.Errorf("expected stable DisplayID across refinements, got %q and %q", firstID, secondID)
	}
	if !strings.HasPrefix(firstID, "plan:") {
		t.Errorf("expected plan-prefixed DisplayID, got %q", firstID)
	}
}

// planDisplayID pulls the plan artifact's DisplayID from the first broadcast
// carrying a plan-kind display payload, failing the test otherwise.
func planDisplayID(t *testing.T, ch *testChannel, label string) string {
	t.Helper()
	for _, msg := range ch.sentMessages() {
		if msg.Display == nil {
			continue
		}
		dm, ok := msg.Display.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := dm["kind"].(string)
		if kind != "plan" {
			continue
		}
		if msg.DisplayID == "" {
			t.Fatalf("%s: plan display broadcast missing DisplayID", label)
		}
		return msg.DisplayID
	}
	t.Fatalf("%s: no plan display broadcast found", label)
	return ""
}

func TestHandlePlanOutput_ConversationalReply(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if strings.Contains(opts.Prompt, "Scout Mode") {
			return &types.RunResult{
				SessionID: "scout-s1",
				Output:    `{"reasoning": "simple question", "explores": [], "user_message": ""}`,
				TokensIn:  10, TokensOut: 5,
			}, nil
		}
		// Return conversational text, no anthem-plan block
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    "The current test coverage looks good. You have 15 test files covering the main API endpoints. I'd suggest focusing on the WebSocket handler next.",
			TokensIn:  80, TokensOut: 40,
		}, nil
	}

	orch, ch := newPlanTestOrch(t, orchRunner)

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] How does the test coverage look?",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	var foundReply bool
	var foundCard bool
	for _, msg := range sent {
		if strings.Contains(msg.Text, "test coverage looks good") {
			foundReply = true
		}
		if strings.Contains(msg.Text, "[plan-card]") {
			foundCard = true
		}
	}
	if !foundReply {
		t.Error("expected conversational text reply for non-plan output")
	}
	if foundCard {
		t.Error("did not expect a plan card for conversational reply")
	}
}

func TestPlanModePromptContents(t *testing.T) {
	required := []string{
		"PLANNING mode",
		"READ-ONLY",
		"Conversational reply",
		"Structured markdown plan",
		"Stage 1: Research",
		"Stage 2: Synthesis",
		"MUST explore",
		"at least 5 tool calls",
		"anthem-plan",
		"todo",
		"Do NOT output JSON actions, HTML, or display artifacts",
	}
	for _, check := range required {
		if !strings.Contains(planModePromptSuffix, check) {
			t.Errorf("planModePromptSuffix missing %q", check)
		}
	}

	forbidden := []string{
		"Rich visual plan",
		"html kind",
		"display action",
	}
	for _, check := range forbidden {
		if strings.Contains(planModePromptSuffix, check) {
			t.Errorf("planModePromptSuffix should NOT contain %q", check)
		}
	}
}

func TestScoutPromptContents(t *testing.T) {
	checks := []string{
		"Scout Mode",
		"explores",
		"query",
		"scope",
		"focus",
		"trivially simple",
		"test coverage",
		"security",
	}
	for _, check := range checks {
		if !strings.Contains(scoutPromptSuffix, check) {
			t.Errorf("scoutPromptSuffix missing %q", check)
		}
	}
}

func TestSynthesisPromptContents(t *testing.T) {
	required := []string{
		"Synthesis Mode",
		"Explorer agents",
		"anthem-plan",
		"MUST be backed by explorer findings",
		"Do NOT output JSON actions, HTML, or display artifacts",
	}
	for _, check := range required {
		if !strings.Contains(synthesisPromptSuffix, check) {
			t.Errorf("synthesisPromptSuffix missing %q", check)
		}
	}

	forbidden := []string{
		"html kind",
		"display action",
		"data-rich analysis",
	}
	for _, check := range forbidden {
		if strings.Contains(synthesisPromptSuffix, check) {
			t.Errorf("synthesisPromptSuffix should NOT contain %q", check)
		}
	}
}

func TestConsultPlan_UsesPlanMaxTurns(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	var capturedOpts types.RunOpts
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		capturedOpts = opts
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    "```anthem-plan\n# Test\n```",
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	oa := NewOrchestratorAgent(orchRunner, "", "", 100000, 10, 25, 10, 5, testLogger())
	_, err := oa.ConsultPlan(context.Background(), StateSnapshot{}, "", nil)
	if err != nil {
		t.Fatalf("ConsultPlan() error: %v", err)
	}

	if capturedOpts.MaxTurns != 25 {
		t.Errorf("ConsultPlan MaxTurns = %d, want 25 (planMaxTurns)", capturedOpts.MaxTurns)
	}

	wantDenied := []string{"Write", "Edit", "MultiEdit"}
	if len(capturedOpts.DeniedTools) != len(wantDenied) {
		t.Fatalf("ConsultPlan DeniedTools = %v, want %v", capturedOpts.DeniedTools, wantDenied)
	}
	for i, tool := range wantDenied {
		if capturedOpts.DeniedTools[i] != tool {
			t.Errorf("ConsultPlan DeniedTools[%d] = %q, want %q", i, capturedOpts.DeniedTools[i], tool)
		}
	}
}

func TestScoutPlan_ParsesExploreRequests(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "scout-s1",
			Output: `{"reasoning": "Need to check tests and security", "explores": [
				{"query": "Check test coverage for auth", "scope": "backend/tests/", "focus": "tests"},
				{"query": "Check path traversal prevention", "scope": "backend/", "focus": "security"}
			], "user_message": "Researching your codebase..."}`,
			TokensIn: 50, TokensOut: 30,
		}, nil
	}

	oa := NewOrchestratorAgent(orchRunner, "", "", 100000, 10, 25, 10, 5, testLogger())
	explores, userMsg, err := oa.ScoutPlan(context.Background(), StateSnapshot{}, "", nil)
	if err != nil {
		t.Fatalf("ScoutPlan() error: %v", err)
	}
	if len(explores) != 2 {
		t.Fatalf("expected 2 explores, got %d", len(explores))
	}
	if explores[0].Focus != "tests" {
		t.Errorf("explore[0].Focus = %q, want tests", explores[0].Focus)
	}
	if explores[1].Focus != "security" {
		t.Errorf("explore[1].Focus = %q, want security", explores[1].Focus)
	}
	if userMsg != "Researching your codebase..." {
		t.Errorf("user_message = %q, want 'Researching your codebase...'", userMsg)
	}
}

func TestScoutPlan_CapsAtMaxExplorers(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "scout-s1",
			Output: `{"reasoning": "many areas", "explores": [
				{"query": "q1", "scope": ".", "focus": "tests"},
				{"query": "q2", "scope": ".", "focus": "security"},
				{"query": "q3", "scope": ".", "focus": "architecture"}
			], "user_message": ""}`,
			TokensIn: 50, TokensOut: 30,
		}, nil
	}

	oa := NewOrchestratorAgent(orchRunner, "", "", 100000, 10, 25, 10, 2, testLogger())
	explores, _, err := oa.ScoutPlan(context.Background(), StateSnapshot{}, "", nil)
	if err != nil {
		t.Fatalf("ScoutPlan() error: %v", err)
	}
	if len(explores) != 2 {
		t.Errorf("expected explores capped at 2 (maxExplorers), got %d", len(explores))
	}
}

func TestParseExplorerFindings(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantSum string
		wantErr bool
	}{
		{
			name:    "valid findings block",
			input:   "Some text\n```explorer-findings\n{\"query\": \"test q\", \"findings\": \"found X\", \"summary\": \"X exists\"}\n```\nMore text",
			wantSum: "X exists",
		},
		{
			name:    "no findings block falls back to raw output",
			input:   "Just raw text about what I found",
			wantSum: "Just raw text about what I found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseExplorerFindings(tt.input)
			if err != nil {
				t.Fatalf("parseExplorerFindings() error: %v", err)
			}
			if result.Summary != tt.wantSum {
				t.Errorf("summary = %q, want %q", result.Summary, tt.wantSum)
			}
		})
	}
}

func TestBuildExplorerPrompt(t *testing.T) {
	req := ExploreRequest{
		Query: "Check test coverage",
		Scope: "backend/tests/",
		Focus: "tests",
	}
	prompt := BuildExplorerPrompt(req, "src/\n  main.go\n  lib/")
	checks := []string{
		"Research Agent",
		"Check test coverage",
		"tests",
		"backend/tests/",
		"explorer-findings",
		"main.go",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("explorer prompt missing %q", check)
		}
	}
}

func TestBuildExplorerPrompt_TestsFocusInstructions(t *testing.T) {
	req := ExploreRequest{
		Query: "What test coverage exists?",
		Scope: "backend/",
		Focus: "tests",
	}
	prompt := BuildExplorerPrompt(req, "")
	checks := []string{
		"route handlers/endpoints have dedicated tests",
		"service-layer tests exist but the endpoint/route",
		"security-critical functions",
		"partially tested files",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("tests focus prompt missing %q", check)
		}
	}
}

func TestBuildExplorerPrompt_SecurityFocusInstructions(t *testing.T) {
	req := ExploreRequest{
		Query: "Audit security boundaries",
		Scope: "backend/",
		Focus: "security",
	}
	prompt := BuildExplorerPrompt(req, "")
	checks := []string{
		"input validation function",
		"path traversal guard",
		"authentication middleware",
		"zero test coverage as CRITICAL",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("security focus prompt missing %q", check)
		}
	}
}

func TestBuildExplorerPrompt_DefaultFocusNoExtra(t *testing.T) {
	req := ExploreRequest{
		Query: "How is the code structured?",
		Scope: "src/",
		Focus: "architecture",
	}
	prompt := BuildExplorerPrompt(req, "")

	forbidden := []string{
		"route handlers/endpoints have dedicated tests",
		"path traversal guard",
		"zero test coverage as CRITICAL",
		"Test Coverage Deep-Dive",
		"Security Boundary Audit",
	}
	for _, check := range forbidden {
		if strings.Contains(prompt, check) {
			t.Errorf("architecture focus prompt should NOT contain %q", check)
		}
	}
}

func TestSynthesizePlan_IncludesFindings(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	var capturedPrompt string
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		capturedPrompt = opts.Prompt
		return &types.RunResult{
			SessionID: "synth-s1",
			Output:    "```anthem-plan\n# Synthesized Plan\n```",
			TokensIn:  50, TokensOut: 30,
		}, nil
	}

	oa := NewOrchestratorAgent(orchRunner, "", "", 100000, 10, 25, 10, 5, testLogger())
	findings := []ExploreResult{
		{Query: "Check tests", Summary: "No tests for /api/costs", Findings: "Detailed finding text"},
		{Query: "Check security", Error: "explorer timed out"},
	}
	output, err := oa.SynthesizePlan(context.Background(), StateSnapshot{}, findings, "", nil)
	if err != nil {
		t.Fatalf("SynthesizePlan() error: %v", err)
	}
	if !strings.Contains(output, "Synthesized Plan") {
		t.Error("expected plan output from synthesis")
	}
	if !strings.Contains(capturedPrompt, "No tests for /api/costs") {
		t.Error("synthesis prompt should include explorer findings summary")
	}
	if !strings.Contains(capturedPrompt, "explorer timed out") {
		t.Error("synthesis prompt should include explorer errors")
	}
	if !strings.Contains(capturedPrompt, "Synthesis Mode") {
		t.Error("synthesis prompt should include synthesis mode suffix")
	}
}

func TestExtractModelTag(t *testing.T) {
	tests := []struct {
		input     string
		wantModel string
		wantClean string
	}{
		{"[model:claude-sonnet-4-6] hello", "claude-sonnet-4-6", "hello"},
		{"[system:fast] [model:claude-opus-4-6] hi", "claude-opus-4-6", "[system:fast] hi"},
		{"no model tag here", "", "no model tag here"},
		{"[model:haiku] ", "haiku", ""},
	}
	for _, tt := range tests {
		model, cleaned := extractModelTag(tt.input)
		if model != tt.wantModel {
			t.Errorf("extractModelTag(%q) model = %q, want %q", tt.input, model, tt.wantModel)
		}
		if cleaned != tt.wantClean {
			t.Errorf("extractModelTag(%q) cleaned = %q, want %q", tt.input, cleaned, tt.wantClean)
		}
	}
}
