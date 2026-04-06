package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/workspace"
)

// echoCommand returns a command name that prints its arguments to stdout,
// cross-platform. On Windows, creates a .bat file in t.TempDir().
func echoCommand(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return "echo"
	}
	bat := filepath.Join(t.TempDir(), "echo.bat")
	if err := os.WriteFile(bat, []byte("@echo off\necho %*\n"), 0o644); err != nil {
		t.Fatalf("failed to create echo.bat: %v", err)
	}
	return bat
}

func TestHandleLeanMessage_StreamsAndReplies(t *testing.T) {
	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	cfg := config.DefaultConfig()
	cfg.Agent.Command = echoCommand(t)
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "",
		Runner:         agent.NewMockRunner(),
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

	cfg := config.DefaultConfig()
	cfg.Agent.Command = "nonexistent-command-that-does-not-exist"
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "",
		Runner:         agent.NewMockRunner(),
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
	cfg.Agent.Command = echoCommand(t)
	cfg.Workspace.Root = ""

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

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		SenderID:    "user-1",
		ThreadID:    "thread-status",
		Text:        "check [system:status] please",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	// Should have ack + stream deltas + stream done + follow-up (lean path)
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
	cfg.Agent.Command = echoCommand(t)
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "",
		Runner:         agent.NewMockRunner(),
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
