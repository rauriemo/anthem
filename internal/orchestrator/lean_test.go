package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/cost"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/workspace"
)

func leanRunner(output string) *agent.MockRunner {
	r := agent.NewMockRunner()
	r.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if opts.OnStream != nil {
			opts.OnStream(output)
		}
		return &types.RunResult{
			SessionID: "lean-sess",
			ExitCode:  0,
			Output:    output,
			CostUSD:   0.01,
			TokensIn:  50,
			TokensOut: 20,
		}, nil
	}
	return r
}

func TestHandleLeanMessage_StreamsAndReplies(t *testing.T) {
	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "",
		Runner:         leanRunner("hello world"),
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		ChannelManager: mgr,
	})

	orch.handleLeanMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		SenderID:    "user-1",
		ThreadID:    "thread-lean",
		Text:        "hello world",
		Timestamp:   time.Now(),
	}, "")

	sent := ch.sentMessages()

	var hasStreamDelta, hasStreamDone, hasFollowUp bool
	for _, msg := range sent {
		if msg.StreamDelta != "" {
			hasStreamDelta = true
			if msg.ThreadID != "thread-lean" {
				t.Errorf("stream delta ThreadID = %q, want thread-lean", msg.ThreadID)
			}
		}
		if msg.StreamDone {
			hasStreamDone = true
			if msg.ThreadID != "thread-lean" {
				t.Errorf("stream done ThreadID = %q, want thread-lean", msg.ThreadID)
			}
		}
		if msg.EventType == "channel.followup" && msg.Text != "" {
			hasFollowUp = true
			if msg.ThreadID != "thread-lean" {
				t.Errorf("follow-up ThreadID = %q, want thread-lean", msg.ThreadID)
			}
		}
	}

	if !hasStreamDelta {
		t.Error("expected at least one stream delta message")
	}
	if !hasStreamDone {
		t.Error("expected a stream done message")
	}
	if !hasFollowUp {
		t.Error("expected a follow-up reply message")
	}
}

func TestHandleLeanMessage_ErrorSendsFollowUp(t *testing.T) {
	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	errRunner := agent.NewMockRunner()
	errRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return nil, errors.New("agent failed")
	}

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "",
		Runner:         errRunner,
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		ChannelManager: mgr,
	})

	orch.handleLeanMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		SenderID:    "user-1",
		ThreadID:    "thread-err",
		Text:        "test",
		Timestamp:   time.Now(),
	}, "")

	sent := ch.sentMessages()
	found := false
	for _, msg := range sent {
		if msg.EventType == "channel.followup" && strings.Contains(msg.Text, "Failed to process") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error follow-up message, got: %+v", sent)
	}
}

func TestHandleLeanMessage_RecordsCost(t *testing.T) {
	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = ""
	ct := cost.NewTracker()

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "",
		Runner:         leanRunner("response"),
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		ChannelManager: mgr,
		CostTracker:    ct,
	})

	orch.handleLeanMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		SenderID:    "user-1",
		ThreadID:    "thread-cost",
		Text:        "test",
		Timestamp:   time.Now(),
	}, "")

	leanCost := ct.TaskCost(leanCostTaskID)
	if leanCost < 0.009 || leanCost > 0.011 {
		t.Errorf("lean cost = %f, want ~0.01", leanCost)
	}
}

func TestHandleLeanMessage_RunOptsMaxTurns(t *testing.T) {
	var capturedOpts types.RunOpts
	r := agent.NewMockRunner()
	r.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		capturedOpts = opts
		return &types.RunResult{ExitCode: 0, Output: "ok"}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "",
		Runner:       r,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	orch.handleLeanMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		Text:        "test",
	}, "")

	if capturedOpts.MaxTurns != cfg.Orchestrator.ChatMaxTurns {
		t.Errorf("MaxTurns = %d, want ChatMaxTurns=%d", capturedOpts.MaxTurns, cfg.Orchestrator.ChatMaxTurns)
	}
	if capturedOpts.MaxTurns != 2 {
		t.Errorf("MaxTurns = %d, want default 2", capturedOpts.MaxTurns)
	}
	if capturedOpts.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q, want bypassPermissions", capturedOpts.PermissionMode)
	}
}

func TestHandleLeanMessage_RunOptsMaxTurnsCustom(t *testing.T) {
	var capturedOpts types.RunOpts
	r := agent.NewMockRunner()
	r.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		capturedOpts = opts
		return &types.RunResult{ExitCode: 0, Output: "ok"}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = ""
	cfg.Orchestrator.ChatMaxTurns = 5

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "",
		Runner:       r,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	orch.handleLeanMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		Text:        "test",
	}, "")

	if capturedOpts.MaxTurns != 5 {
		t.Errorf("MaxTurns = %d, want 5 (from custom config)", capturedOpts.MaxTurns)
	}
}

func TestHandleLeanMessage_RunOptsDeniedTools(t *testing.T) {
	var capturedOpts types.RunOpts
	r := agent.NewMockRunner()
	r.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		capturedOpts = opts
		return &types.RunResult{ExitCode: 0, Output: "ok"}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "",
		Runner:       r,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	orch.handleLeanMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		Text:        "test",
	}, "")

	want := map[string]bool{"Write": false, "Edit": false, "MultiEdit": false}
	for _, tool := range capturedOpts.DeniedTools {
		if _, ok := want[tool]; ok {
			want[tool] = true
		}
	}
	for tool, seen := range want {
		if !seen {
			t.Errorf("DeniedTools missing %q; got %v", tool, capturedOpts.DeniedTools)
		}
	}
}

func TestHandleUserMessage_SystemStatusRoutesToLean(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Title: "Task 1", Status: types.StatusQueued, Labels: []string{"todo"}, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		t.Fatal("orchestrator agent should not be consulted for [system:status] messages")
		return nil, nil
	}

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchAgent := NewOrchestratorAgent(orchRunner, "", "", 100000, 0, 0, 0, 0, testLogger())

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "{{.issue.title}}",
		Tracker:        trk,
		Runner:         leanRunner("status response"),
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		OrchAgent:      orchAgent,
		ChannelManager: mgr,
	})

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		SenderID:    "user-1",
		ThreadID:    "thread-status",
		Text:        "check [system:status] please",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	var hasAck, hasStreamDone bool
	for _, msg := range sent {
		if msg.Ack {
			hasAck = true
		}
		if msg.StreamDone {
			hasStreamDone = true
		}
	}
	if !hasAck {
		t.Error("expected ack message")
	}
	if !hasStreamDone {
		t.Error("expected stream done from lean path")
	}
}

func TestHandleUserMessage_SystemFastRoutesToLean(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Title: "Task 1", Status: types.StatusQueued, Labels: []string{"todo"}, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		t.Fatal("orchestrator agent should not be consulted for [system:fast] messages")
		return nil, nil
	}

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchAgent := NewOrchestratorAgent(orchRunner, "", "", 100000, 0, 0, 0, 0, testLogger())

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "{{.issue.title}}",
		Tracker:        trk,
		Runner:         leanRunner("fast response"),
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		OrchAgent:      orchAgent,
		ChannelManager: mgr,
	})

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		SenderID:    "user-1",
		ThreadID:    "thread-fast",
		Text:        "[system:fast] hey there!",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	var hasAck, hasStreamDone bool
	for _, msg := range sent {
		if msg.Ack {
			hasAck = true
		}
		if msg.StreamDone {
			hasStreamDone = true
		}
	}
	if !hasAck {
		t.Error("expected ack message")
	}
	if !hasStreamDone {
		t.Error("expected stream done from lean path")
	}
}

func TestHandleUserMessage_NilTrackerRoutesToLean(t *testing.T) {
	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "",
		Runner:         leanRunner("lean response"),
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		ChannelManager: mgr,
	})

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		SenderID:    "user-1",
		ThreadID:    "thread-notrk",
		Text:        "hello",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	var hasAck, hasStreamDone bool
	for _, msg := range sent {
		if msg.Ack {
			hasAck = true
		}
		if msg.StreamDone {
			hasStreamDone = true
		}
	}
	if !hasAck {
		t.Error("expected ack message")
	}
	if !hasStreamDone {
		t.Error("expected stream done from lean path (nil tracker)")
	}
}

func TestExtractLeanDisplayBlocks_SingleHTML(t *testing.T) {
	input := "Here is the report:\n\n```prism-display\n{\"kind\":\"html\",\"content\":\"<h1>Hello</h1>\"}\n```\n\nDone."
	clean, displays := extractLeanDisplayBlocks(input)

	if len(displays) != 1 {
		t.Fatalf("expected 1 display, got %d", len(displays))
	}
	if displays[0]["kind"] != "html" {
		t.Errorf("expected kind=html, got %v", displays[0]["kind"])
	}
	if displays[0]["content"] != "<h1>Hello</h1>" {
		t.Errorf("unexpected content: %v", displays[0]["content"])
	}
	if strings.Contains(clean, "prism-display") {
		t.Error("display block should be stripped from clean text")
	}
	if !strings.Contains(clean, "Here is the report:") {
		t.Error("surrounding text should be preserved")
	}
	if !strings.Contains(clean, "Done.") {
		t.Error("trailing text should be preserved")
	}
}

func TestExtractLeanDisplayBlocks_MultipleBlocks(t *testing.T) {
	input := "Text before\n```prism-display\n{\"kind\":\"data\",\"title\":\"Table\"}\n```\nMiddle\n```prism-display\n{\"kind\":\"chart\",\"title\":\"Graph\"}\n```\nEnd"
	clean, displays := extractLeanDisplayBlocks(input)

	if len(displays) != 2 {
		t.Fatalf("expected 2 displays, got %d", len(displays))
	}
	if displays[0]["kind"] != "data" {
		t.Errorf("first kind = %v, want data", displays[0]["kind"])
	}
	if displays[1]["kind"] != "chart" {
		t.Errorf("second kind = %v, want chart", displays[1]["kind"])
	}
	if strings.Contains(clean, "prism-display") {
		t.Error("display blocks should be stripped")
	}
	if !strings.Contains(clean, "Middle") {
		t.Error("middle text should be preserved")
	}
}

func TestExtractLeanDisplayBlocks_NoBlocks(t *testing.T) {
	input := "Just regular text\nwith no display blocks."
	clean, displays := extractLeanDisplayBlocks(input)

	if len(displays) != 0 {
		t.Errorf("expected 0 displays, got %d", len(displays))
	}
	if !strings.Contains(clean, "Just regular text") {
		t.Error("text should pass through unchanged")
	}
}

func TestExtractLeanDisplayBlocks_InvalidJSON(t *testing.T) {
	input := "Before\n```prism-display\nnot valid json\n```\nAfter"
	clean, displays := extractLeanDisplayBlocks(input)

	if len(displays) != 1 {
		t.Fatalf("invalid JSON in prism-display should fall back to html, got %d displays", len(displays))
	}
	if displays[0]["kind"] != "html" {
		t.Errorf("fallback kind = %v, want html", displays[0]["kind"])
	}
	if !strings.Contains(clean, "Before") || !strings.Contains(clean, "After") {
		t.Error("surrounding text should be preserved")
	}
}

func TestExtractLeanDisplayBlocks_MissingKind(t *testing.T) {
	input := "```prism-display\n{\"content\":\"no kind field\"}\n```"
	_, displays := extractLeanDisplayBlocks(input)

	// Valid JSON without "kind" — falls back to html with raw content
	if len(displays) != 1 {
		t.Fatalf("missing-kind JSON should fall back to html, got %d displays", len(displays))
	}
	if displays[0]["kind"] != "html" {
		t.Errorf("fallback kind = %v, want html", displays[0]["kind"])
	}
}

func TestExtractLeanDisplayBlocks_HTMLBlock(t *testing.T) {
	input := "Here is the visual:\n\n```html\n<div style=\"padding:1rem\"><h1>Story Arc</h1></div>\n```\n\nThat's the overview."
	clean, displays := extractLeanDisplayBlocks(input)

	if len(displays) != 1 {
		t.Fatalf("expected 1 display from html block, got %d", len(displays))
	}
	if displays[0]["kind"] != "html" {
		t.Errorf("kind = %v, want html", displays[0]["kind"])
	}
	content, _ := displays[0]["content"].(string)
	if !strings.Contains(content, "<h1>Story Arc</h1>") {
		t.Errorf("html content not captured: %v", content)
	}
	if strings.Contains(clean, "<div") {
		t.Error("html block should be stripped from clean text")
	}
	if !strings.Contains(clean, "Here is the visual:") || !strings.Contains(clean, "That's the overview.") {
		t.Error("surrounding text should be preserved")
	}
}

func TestExtractLeanDisplayBlocks_MarkdownBlock(t *testing.T) {
	input := "Summary:\n```markdown\n# Chapter 1\n\nThe hero sets out.\n```\nEnd."
	clean, displays := extractLeanDisplayBlocks(input)

	if len(displays) != 1 {
		t.Fatalf("expected 1 display from markdown block, got %d", len(displays))
	}
	if displays[0]["kind"] != "markdown" {
		t.Errorf("kind = %v, want markdown", displays[0]["kind"])
	}
	content, _ := displays[0]["content"].(string)
	if !strings.Contains(content, "# Chapter 1") {
		t.Errorf("markdown content not captured: %v", content)
	}
	if !strings.Contains(clean, "End.") {
		t.Error("trailing text should be preserved")
	}
}

func TestExtractLeanDisplayBlocks_MdAlias(t *testing.T) {
	input := "```md\n## Section\n\nContent here.\n```"
	_, displays := extractLeanDisplayBlocks(input)

	if len(displays) != 1 {
		t.Fatalf("expected 1 display from md block, got %d", len(displays))
	}
	if displays[0]["kind"] != "markdown" {
		t.Errorf("kind = %v, want markdown", displays[0]["kind"])
	}
}

func TestExtractLeanDisplayBlocks_UnclosedBlock(t *testing.T) {
	input := "Before\n```html\n<h1>Unclosed</h1>"
	clean, displays := extractLeanDisplayBlocks(input)

	if len(displays) != 0 {
		t.Errorf("unclosed block should not produce display, got %d", len(displays))
	}
	if !strings.Contains(clean, "<h1>Unclosed</h1>") {
		t.Error("unclosed block content should be preserved in clean text")
	}
}

func TestExtractLeanDisplayBlocks_MixedBlockTypes(t *testing.T) {
	input := "Text\n```prism-display\n{\"kind\":\"html\",\"content\":\"<b>bold</b>\"}\n```\nMiddle\n```html\n<p>raw html</p>\n```\nEnd"
	clean, displays := extractLeanDisplayBlocks(input)

	if len(displays) != 2 {
		t.Fatalf("expected 2 displays, got %d", len(displays))
	}
	if displays[0]["kind"] != "html" || displays[0]["content"] != "<b>bold</b>" {
		t.Errorf("first display unexpected: %v", displays[0])
	}
	if displays[1]["kind"] != "html" {
		t.Errorf("second kind = %v, want html", displays[1]["kind"])
	}
	content, _ := displays[1]["content"].(string)
	if !strings.Contains(content, "<p>raw html</p>") {
		t.Errorf("second content unexpected: %v", content)
	}
	if !strings.Contains(clean, "Middle") || !strings.Contains(clean, "End") {
		t.Error("surrounding text should be preserved")
	}
}

func TestRun_ChannelOnlyMode(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Polling.IntervalMS = 100

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "",
		Runner:       agent.NewMockRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := orch.Run(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
}

func TestBuildLeanPrompt_IncludesProjectContext(t *testing.T) {
	projectCtx := &ProjectContext{ProjectSummary: "A test project."}
	msg := channel.IncomingMessage{Text: "hello"}

	prompt := buildLeanPrompt(projectCtx, "", "", "", msg)

	if !strings.Contains(prompt, "A test project.") {
		t.Error("expected project context in prompt")
	}
	if !strings.Contains(prompt, "hello") {
		t.Error("expected user message in prompt")
	}
	if !strings.Contains(prompt, "Chat mode") {
		t.Error("expected Chat mode constraint in prompt")
	}
	if !strings.Contains(prompt, "ONE read-only tool call") {
		t.Error("expected one-tool-call constraint in prompt")
	}
	if strings.Contains(prompt, "FAST mode") {
		t.Error("legacy 'FAST mode' phrasing should be gone")
	}
	if strings.Contains(prompt, "Do NOT use tools") {
		t.Error("legacy 'Do NOT use tools' phrasing should be gone")
	}
}

func TestBuildLeanPrompt_PrismDisplayInstructions(t *testing.T) {
	msg := channel.IncomingMessage{Text: "test", ChannelKind: "prism"}
	prompt := buildLeanPrompt(nil, "", "", "", msg)

	if !strings.Contains(prompt, "prism-display") {
		t.Error("expected prism display instructions in prompt")
	}
}

func TestBuildLeanPrompt_ToolBudgetLanguage(t *testing.T) {
	msg := channel.IncomingMessage{Text: "hi"}
	prompt := buildLeanPrompt(nil, "", "", "", msg)

	if !strings.Contains(prompt, "ONE read-only tool call") {
		t.Error("expected 'ONE read-only tool call' in prompt")
	}
	if !strings.Contains(prompt, "prefer answering from") {
		t.Error("expected 'prefer answering from' hydrated-context phrasing in prompt")
	}
	if !strings.Contains(prompt, "Write tools") {
		t.Error("expected mention of disabled Write tools")
	}
	if !strings.Contains(prompt, "Plan or Execute mode") {
		t.Error("expected guidance toward Plan/Execute mode for edits")
	}
}

func TestBuildLeanPrompt_HydratesFeatureContext(t *testing.T) {
	msg := channel.IncomingMessage{Text: "what's the plan"}
	featureCtx := "## Feature Context: demo\n\n### Plan Summary\nBuild a thing."

	prompt := buildLeanPrompt(nil, featureCtx, "", "", msg)

	if !strings.Contains(prompt, "## Feature Context") {
		t.Error("expected Feature Context header in prompt")
	}
	if !strings.Contains(prompt, "Build a thing.") {
		t.Error("expected hydrated feature content in prompt")
	}
}

func TestBuildLeanPrompt_HydratesSharedContext(t *testing.T) {
	msg := channel.IncomingMessage{Text: "what did we decide"}
	sharedCtx := "We agreed to ship by Friday."

	prompt := buildLeanPrompt(nil, "", sharedCtx, "", msg)

	if !strings.Contains(prompt, "## Shared Context") {
		t.Error("expected Shared Context header in prompt")
	}
	if !strings.Contains(prompt, "We agreed to ship by Friday.") {
		t.Error("expected shared context body in prompt")
	}
}

func TestBuildLeanPrompt_NoHydratedContext_Graceful(t *testing.T) {
	msg := channel.IncomingMessage{Text: "hello"}
	prompt := buildLeanPrompt(nil, "", "", "", msg)

	if strings.Contains(prompt, "## Feature Context") {
		t.Error("Feature Context section should be omitted when empty")
	}
	if strings.Contains(prompt, "## Shared Context") {
		t.Error("Shared Context section should be omitted when empty")
	}
	if !strings.Contains(prompt, "hello") {
		t.Error("user message should still be present")
	}
}

func TestBuildLeanPrompt_WhitespaceOnlyHydrated_Omitted(t *testing.T) {
	msg := channel.IncomingMessage{Text: "hello"}
	prompt := buildLeanPrompt(nil, "   \n\t  ", "\n\n", " \t", msg)

	if strings.Contains(prompt, "## Feature Context") {
		t.Error("Feature Context section should be omitted when whitespace-only")
	}
	if strings.Contains(prompt, "## Shared Context") {
		t.Error("Shared Context section should be omitted when whitespace-only")
	}
	if strings.Contains(prompt, "## User Context") {
		t.Error("User Context section should be omitted when whitespace-only")
	}
}

func TestBuildLeanPrompt_HydratesUserContext(t *testing.T) {
	msg := channel.IncomingMessage{Text: "hi"}
	userCtx := "Rafael prefers terse, evidence-backed answers."

	prompt := buildLeanPrompt(nil, "", "", userCtx, msg)

	if !strings.Contains(prompt, "## User Context") {
		t.Error("expected User Context header in prompt")
	}
	if !strings.Contains(prompt, "Rafael prefers terse") {
		t.Error("expected user context body in prompt")
	}
}

func TestHandleLeanMessage_HydratesVoiceContentIntoPrompt(t *testing.T) {
	var capturedOpts types.RunOpts
	r := agent.NewMockRunner()
	r.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		capturedOpts = opts
		return &types.RunResult{ExitCode: 0, Output: "ok"}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "",
		Runner:       r,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
		VoiceContent: "## About Rafael\n\nRafael prefers concise answers and evidence citations.",
	})

	orch.handleLeanMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		Text:        "hi",
	}, "")

	if !strings.Contains(capturedOpts.Prompt, "## User Context") {
		t.Error("prompt missing User Context section from VOICE.md hydration")
	}
	if !strings.Contains(capturedOpts.Prompt, "Rafael prefers concise answers") {
		t.Error("prompt missing voice content body")
	}
}

func TestHandleLeanMessage_HydratesFeatureContextIntoPrompt(t *testing.T) {
	var capturedOpts types.RunOpts
	r := agent.NewMockRunner()
	r.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		capturedOpts = opts
		return &types.RunResult{ExitCode: 0, Output: "ok"}, nil
	}

	tmp := t.TempDir()
	featureDir := filepath.Join(tmp, ".context", "features", "demo")
	if err := os.MkdirAll(featureDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	planContent := "---\nschema_version: \"1\"\nfeature: demo\nphase: scene-layout\nowner: user\n---\n\n# Demo Feature\n\nShip a tower defense level.\n"
	if err := os.WriteFile(filepath.Join(featureDir, "plan.md"), []byte(planContent), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	changelogContent := "schema_version: \"1\"\nentries:\n  - id: log-001\n    timestamp: \"2026-04-10T14:30:00Z\"\n    agent: miyazaki\n    action: asset_created\n    summary: \"Goblin sprite created.\"\n    tags: [sprite]\n"
	if err := os.WriteFile(filepath.Join(featureDir, "changelog.yaml"), []byte(changelogContent), 0o644); err != nil {
		t.Fatalf("write changelog: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = tmp
	cfg.ActiveFeature = "demo"

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "",
		Runner:       r,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	orch.handleLeanMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		Text:        "what changed recently",
	}, "")

	if !strings.Contains(capturedOpts.Prompt, "## Feature Context") {
		t.Error("prompt missing Feature Context section from hydration")
	}
	if !strings.Contains(capturedOpts.Prompt, "Goblin sprite created.") {
		t.Error("prompt missing hydrated changelog entry")
	}
}

func TestHandleLeanMessage_MissingFeatureDir_Graceful(t *testing.T) {
	var capturedOpts types.RunOpts
	r := agent.NewMockRunner()
	r.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		capturedOpts = opts
		return &types.RunResult{ExitCode: 0, Output: "ok"}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = t.TempDir()
	cfg.ActiveFeature = "nonexistent-feature"

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "",
		Runner:       r,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	orch.handleLeanMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		Text:        "test",
	}, "")

	if capturedOpts.Prompt == "" {
		t.Fatal("expected prompt even with missing feature dir")
	}
	if strings.Contains(capturedOpts.Prompt, "## Feature Context") {
		t.Error("Feature Context section should be omitted when dir missing")
	}
}

func TestHandleLeanMessage_HydratesSharedContextIntoPrompt(t *testing.T) {
	var capturedOpts types.RunOpts
	r := agent.NewMockRunner()
	r.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		capturedOpts = opts
		return &types.RunResult{ExitCode: 0, Output: "ok"}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "",
		Runner:       r,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	orch.sharedCtx.Update("test", "Shared decision: ship by Friday.")

	orch.handleLeanMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		Text:        "ping",
	}, "")

	if !strings.Contains(capturedOpts.Prompt, "## Shared Context") {
		t.Error("prompt missing Shared Context section")
	}
	if !strings.Contains(capturedOpts.Prompt, "ship by Friday") {
		t.Error("prompt missing shared context content")
	}
}
