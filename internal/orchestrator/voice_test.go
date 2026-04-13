package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/guests"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/workspace"
)

// failOnListTracker wraps MockTracker and fails the test if ListActive is called.
type failOnListTracker struct {
	*tracker.MockTracker
	t *testing.T
}

func (f *failOnListTracker) ListActive(_ context.Context) ([]types.Task, error) {
	f.t.Fatal("ListActive should not be called for voice messages")
	return nil, nil
}

func TestHandleUserMessage_VoiceSkipsTaskListing(t *testing.T) {
	trk := &failOnListTracker{
		MockTracker: tracker.NewMockTracker([]types.Task{
			{ID: "1", Title: "Task 1", Status: types.StatusQueued, Labels: []string{"todo"}, CreatedAt: time.Now()},
		}),
		t: t,
	}

	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "orch-voice-1",
			Output:    `{"reasoning": "voice reply", "actions": [{"type": "reply", "body": "Got it."}]}`,
			TokensIn:  10, TokensOut: 5,
		}, nil
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
		Runner:         agent.NewMockRunner(),
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		OrchAgent:      orchAgent,
		ChannelManager: mgr,
	})

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "voice",
		SenderID:    "user-1",
		ThreadID:    "thread-voice-1",
		Text:        "What's the project status?",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	var hasAck, hasStreamDone, hasReply bool
	for _, msg := range sent {
		if msg.Ack {
			hasAck = true
		}
		if msg.StreamDone {
			hasStreamDone = true
		}
		if msg.EventType == "channel.followup" && msg.Text == "Got it." {
			hasReply = true
		}
	}
	if !hasAck {
		t.Error("expected ack message")
	}
	if !hasStreamDone {
		t.Error("expected stream done from voice path")
	}
	if !hasReply {
		t.Error("expected follow-up reply 'Got it.'")
	}
}

func TestHandleUserMessage_VoiceSkipsGuestRouting(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "orch-voice-2",
			Output:    `{"reasoning": "voice answer", "actions": [{"type": "reply", "body": "Here you go."}]}`,
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	guestRunner := agent.NewMockRunner()
	guestRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		t.Fatal("guest routing runner should not be called for voice messages")
		return nil, nil
	}

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchAgent := NewOrchestratorAgent(orchRunner, "", "", 100000, 0, 0, 0, 0, testLogger())

	idx := &guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"noah": {Name: "Noah", Description: "Problem solver"},
		},
	}

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "",
		Runner:         guestRunner,
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		OrchAgent:      orchAgent,
		ChannelManager: mgr,
	})
	orch.guestIndex = idx

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind:  "voice",
		SenderID:     "user-1",
		ThreadID:     "thread-voice-2",
		Text:         "Can you help me with something?",
		ActiveGuests: []string{"noah"},
		Timestamp:    time.Now(),
	})

	sent := ch.sentMessages()
	var hasReply bool
	for _, msg := range sent {
		if msg.EventType == "channel.followup" && msg.Text == "Here you go." {
			hasReply = true
		}
	}
	if !hasReply {
		t.Error("expected follow-up reply 'Here you go.'")
	}
}

func TestHandleUserMessage_VoiceCanInviteGuest(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "orch-voice-3",
			Output: `{"reasoning": "user wants to invite noah", "actions": [
				{"type": "reply", "body": "I'll invite Noah to help."},
				{"type": "create_subtasks", "subtasks": [{"title": "Help with animation", "body": "Noah should assist with animation system"}]}
			]}`,
			TokensIn: 20, TokensOut: 15,
		}, nil
	}

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchAgent := NewOrchestratorAgent(orchRunner, "", "", 100000, 0, 0, 0, 0, testLogger())

	trk := tracker.NewMockTracker(nil)

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "",
		Tracker:        trk,
		Runner:         agent.NewMockRunner(),
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		OrchAgent:      orchAgent,
		ChannelManager: mgr,
	})

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "voice",
		SenderID:    "user-1",
		ThreadID:    "thread-voice-3",
		Text:        "invite Noah to help with the animation system",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	var hasReply bool
	for _, msg := range sent {
		if msg.EventType == "channel.followup" && msg.Text == "I'll invite Noah to help." {
			hasReply = true
		}
	}
	if !hasReply {
		t.Errorf("expected follow-up reply about inviting Noah, got messages: %+v", sent)
	}

	// Verify create_subtasks action was processed by checking the tracker
	if len(trk.Tasks) == 0 {
		t.Error("expected create_subtasks to create at least one issue via the tracker")
	}
}

func TestHandleUserMessage_VoiceFallsBackToLeanWithoutOrchAgent(t *testing.T) {
	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = ""

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "",
		Runner:         leanRunner("lean voice response"),
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		ChannelManager: mgr,
	})

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "voice",
		SenderID:    "user-1",
		ThreadID:    "thread-voice-lean",
		Text:        "Hello",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	var hasStreamDone bool
	for _, msg := range sent {
		if msg.StreamDone {
			hasStreamDone = true
		}
	}
	if !hasStreamDone {
		t.Error("expected stream done from lean fallback path")
	}
}
