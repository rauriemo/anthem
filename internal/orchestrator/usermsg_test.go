package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/workspace"
)

type testChannel struct {
	sent []channel.OutgoingMessage
	in   chan channel.IncomingMessage
	mu   sync.Mutex
}

func newTestChannel() *testChannel {
	return &testChannel{in: make(chan channel.IncomingMessage, 16)}
}

func (c *testChannel) Kind() string                             { return "test" }
func (c *testChannel) Start(_ context.Context) error            { return nil }
func (c *testChannel) Incoming() <-chan channel.IncomingMessage { return c.in }
func (c *testChannel) Close() error                             { return nil }
func (c *testChannel) Send(_ context.Context, msg channel.OutgoingMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, msg)
	return nil
}

func (c *testChannel) sentMessages() []channel.OutgoingMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]channel.OutgoingMessage, len(c.sent))
	copy(cp, c.sent)
	return cp
}

func TestHandleUserMessage_ConsultsAndReplies(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Title: "Task 1", Status: types.StatusQueued, Labels: []string{"todo"}, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "orch-s1",
			Output:    `{"reasoning": "user asked about tasks", "actions": [{"type": "reply", "body": "There is 1 task queued."}]}`,
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, testLogger())

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"

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
		ThreadID:    "thread-123",
		Text:        "What tasks are pending?",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	if len(sent) == 0 {
		t.Fatal("expected at least one reply message")
	}

	found := false
	for _, msg := range sent {
		if msg.Text == "There is 1 task queued." {
			found = true
			if msg.ThreadID != "thread-123" {
				t.Errorf("ThreadID = %q, want thread-123", msg.ThreadID)
			}
			if !msg.Markdown {
				t.Error("expected Markdown = true")
			}
		}
	}
	if !found {
		t.Errorf("expected reply 'There is 1 task queued.' in sent messages: %+v", sent)
	}
}

func TestHandleUserMessage_TextFileIncluded(t *testing.T) {
	msg := channel.IncomingMessage{
		Text: "Deploy this",
		Files: []channel.File{
			{Name: "plan.md", Content: []byte("# Deploy Plan\nStep 1"), MimeType: "text/markdown"},
		},
	}
	umc := buildUserMessageContext(msg)

	if umc.Text != "Deploy this" {
		t.Errorf("Text = %q, want 'Deploy this'", umc.Text)
	}
	if len(umc.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(umc.Files))
	}
	if umc.Files[0] != "# Deploy Plan\nStep 1" {
		t.Errorf("file content = %q, want plan content", umc.Files[0])
	}
}

func TestHandleUserMessage_ImagePlaceholder(t *testing.T) {
	msg := channel.IncomingMessage{
		Text: "Here's a diagram",
		Files: []channel.File{
			{Name: "arch.png", Content: []byte{0x89, 0x50}, MimeType: "image/png"},
		},
	}
	umc := buildUserMessageContext(msg)

	if len(umc.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(umc.Files))
	}
	if umc.Files[0] != "[image: arch.png]" {
		t.Errorf("file = %q, want '[image: arch.png]'", umc.Files[0])
	}
}

func TestHandleUserMessage_BinaryPlaceholder(t *testing.T) {
	msg := channel.IncomingMessage{
		Text: "Check this zip",
		Files: []channel.File{
			{Name: "data.zip", Content: []byte{0x50, 0x4B}, MimeType: "application/zip"},
		},
	}
	umc := buildUserMessageContext(msg)

	if len(umc.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(umc.Files))
	}
	if umc.Files[0] != "[file: data.zip, type: application/zip]" {
		t.Errorf("file = %q, want placeholder", umc.Files[0])
	}
}

func TestHandleUserMessage_OrchestratorFails(t *testing.T) {
	trk := tracker.NewMockTracker(nil)

	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "orch-fail",
			Output:    "not json",
			TokensIn:  10, TokensOut: 5,
		}, nil
	}
	orchRunner.ContinueFunc = func(_ context.Context, _ string, _ string, _ types.ContinueOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "orch-fail",
			Output:    "still not json",
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, testLogger())

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"

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
		Text:        "What's happening?",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	if len(sent) == 0 {
		t.Fatal("expected error reply")
	}
	found := false
	for _, msg := range sent {
		if msg.Text == "I couldn't understand your request. Please try again." {
			found = true
		}
	}
	if !found {
		t.Errorf("expected fallback error message in: %+v", sent)
	}
}

func TestHandleUserMessage_JSONFileIncluded(t *testing.T) {
	msg := channel.IncomingMessage{
		Text: "Check this config",
		Files: []channel.File{
			{Name: "config.json", Content: []byte(`{"key": "value"}`), MimeType: "application/json"},
		},
	}
	umc := buildUserMessageContext(msg)

	if len(umc.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(umc.Files))
	}
	if umc.Files[0] != `{"key": "value"}` {
		t.Errorf("file = %q, want JSON content", umc.Files[0])
	}
}
