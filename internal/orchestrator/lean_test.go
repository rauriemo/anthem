package orchestrator

import (
	"context"
	"errors"
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
	})

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
	})

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
	})

	leanCost := ct.TaskCost(leanCostTaskID)
	if leanCost < 0.009 || leanCost > 0.011 {
		t.Errorf("lean cost = %f, want ~0.01", leanCost)
	}
}

func TestHandleLeanMessage_MaxTurnsOne(t *testing.T) {
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
	})

	if capturedOpts.MaxTurns != 1 {
		t.Errorf("MaxTurns = %d, want 1", capturedOpts.MaxTurns)
	}
	if capturedOpts.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q, want bypassPermissions", capturedOpts.PermissionMode)
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

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, testLogger())

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

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, testLogger())

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

	if len(displays) != 0 {
		t.Errorf("invalid JSON should not produce display, got %d", len(displays))
	}
	if !strings.Contains(clean, "Before") || !strings.Contains(clean, "After") {
		t.Error("surrounding text should be preserved even with invalid block")
	}
}

func TestExtractLeanDisplayBlocks_MissingKind(t *testing.T) {
	input := "```prism-display\n{\"content\":\"no kind field\"}\n```"
	_, displays := extractLeanDisplayBlocks(input)

	if len(displays) != 0 {
		t.Errorf("JSON without 'kind' should not produce display, got %d", len(displays))
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

	prompt := buildLeanPrompt(projectCtx, msg)

	if !strings.Contains(prompt, "A test project.") {
		t.Error("expected project context in prompt")
	}
	if !strings.Contains(prompt, "hello") {
		t.Error("expected user message in prompt")
	}
}

func TestBuildLeanPrompt_PrismDisplayInstructions(t *testing.T) {
	msg := channel.IncomingMessage{Text: "test", ChannelKind: "prism"}
	prompt := buildLeanPrompt(nil, msg)

	if !strings.Contains(prompt, "prism-display") {
		t.Error("expected prism display instructions in prompt")
	}
}
