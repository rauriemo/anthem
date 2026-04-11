package orchestrator

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
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

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, 0, 0, 0, 0, testLogger())

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
	if len(sent) < 2 {
		t.Fatalf("expected at least 2 messages (ack + reply), got %d: %+v", len(sent), sent)
	}

	// First message should be a protocol-level ack with ThreadID and no text.
	ack := sent[0]
	if ack.ThreadID != "thread-123" {
		t.Errorf("ack ThreadID = %q, want thread-123", ack.ThreadID)
	}
	if !ack.Ack {
		t.Error("ack.Ack = false, want true")
	}
	if ack.Text != "" {
		t.Errorf("ack Text = %q, want empty", ack.Text)
	}

	// The real reply arrives as a channel.followup event carrying the original ThreadID.
	found := false
	for _, msg := range sent[1:] {
		if msg.Text == "There is 1 task queued." {
			found = true
			if msg.EventType != "channel.followup" {
				t.Errorf("reply EventType = %q, want channel.followup", msg.EventType)
			}
			if msg.ThreadID != "thread-123" {
				t.Errorf("reply ThreadID = %q, want thread-123", msg.ThreadID)
			}
		}
	}
	if !found {
		t.Errorf("expected follow-up reply 'There is 1 task queued.' in sent messages: %+v", sent)
	}
}

func TestHandleUserMessage_PrismSkipsReplyWhenDisplayPresent(t *testing.T) {
	tasks := []types.Task{}
	trk := tracker.NewMockTracker(tasks)

	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "orch-s1",
			Output: `{"reasoning": "idle", "actions": [
				{"type": "reply", "body": ""},
				{"type": "display", "display_kind": "html", "display_content": "<p>visual only</p>"}
			]}`,
			TokensIn: 10, TokensOut: 5,
		}, nil
	}

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, 0, 0, 0, 0, testLogger())

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
		ChannelKind: "prism",
		SenderID:    "prism",
		ThreadID:    "thread-prism-1",
		Text:        "status?",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()
	for _, msg := range sent {
		if msg.EventType == "channel.followup" && msg.Text != "" {
			t.Fatalf("prism chat should not get non-empty reply when display is present with empty body: %+v", msg)
		}
	}

	var sawDisplay bool
	for _, msg := range sent {
		if msg.Display != nil {
			sawDisplay = true
			m, ok := msg.Display.(map[string]any)
			if !ok || m["kind"] != "html" {
				t.Fatalf("expected html display map, got %#v", msg.Display)
			}
		}
	}
	if !sawDisplay {
		t.Fatalf("expected a display message in sent: %+v", sent)
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

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, 0, 0, 0, 0, testLogger())

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

func TestHandleUserMessage_StreamsDeltasAndDone(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Title: "Task 1", Status: types.StatusQueued, Labels: []string{"todo"}, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		// Simulate streaming deltas during Run
		if opts.OnStream != nil {
			opts.OnStream("Hello ")
			opts.OnStream("there.")
		}
		return &types.RunResult{
			SessionID: "orch-s1",
			Output:    `{"reasoning": "greeting", "actions": [{"type": "reply", "body": "Hello there."}]}`,
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, 0, 0, 0, 0, testLogger())

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
		ThreadID:    "thread-stream",
		Text:        "Hi",
		Timestamp:   time.Now(),
	})

	sent := ch.sentMessages()

	// Expect: ack, stream delta "Hello ", stream delta "there.", stream done, follow-up reply
	var streamDeltas []string
	streamDoneCount := 0
	for _, msg := range sent {
		if msg.StreamDelta != "" {
			streamDeltas = append(streamDeltas, msg.StreamDelta)
			if msg.ThreadID != "thread-stream" {
				t.Errorf("stream delta ThreadID = %q, want thread-stream", msg.ThreadID)
			}
		}
		if msg.StreamDone {
			streamDoneCount++
			if msg.ThreadID != "thread-stream" {
				t.Errorf("stream done ThreadID = %q, want thread-stream", msg.ThreadID)
			}
		}
	}

	if len(streamDeltas) != 2 {
		t.Fatalf("expected 2 stream deltas, got %d: %v", len(streamDeltas), streamDeltas)
	}
	if streamDeltas[0] != "Hello " || streamDeltas[1] != "there." {
		t.Errorf("unexpected deltas: %v", streamDeltas)
	}
	if streamDoneCount != 1 {
		t.Errorf("expected 1 stream done, got %d", streamDoneCount)
	}
}

func TestHandleUserMessage_SkipsOrchestratorWhenGuestsActive(t *testing.T) {
	var orchCalls int64

	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		atomic.AddInt64(&orchCalls, 1)
		return &types.RunResult{
			SessionID: "orch-s1",
			Output:    `{"reasoning": "idle", "actions": []}`,
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	guestRunner := agent.NewMockRunner()
	guestRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "guest-s1",
			Output:    "Guest response",
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, 0, 0, 0, 0, testLogger())

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "{{.issue.title}}",
		Tracker:        tracker.NewMockTracker(nil),
		Runner:         guestRunner,
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		OrchAgent:      orchAgent,
		ChannelManager: mgr,
	})

	orch.guestIndex = &guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"writer-1": {ID: "writer-1", Name: "Story Writer", Description: "Writes stories"},
		},
	}

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind:  "test",
		SenderID:     "user-1",
		ThreadID:     "thread-guest",
		Text:         "Write a chapter",
		ActiveGuests: []string{"writer-1"},
		Timestamp:    time.Now(),
	})

	if atomic.LoadInt64(&orchCalls) != 0 {
		t.Errorf("expected orchestrator runner to NOT be called, but got %d calls", orchCalls)
	}
}

func TestHandleUserMessage_IncludesOrchestratorWithSystemTag(t *testing.T) {
	var leanCalls int64

	generalRunner := agent.NewMockRunner()
	generalRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		atomic.AddInt64(&leanCalls, 1)
		if opts.OnStream != nil {
			opts.OnStream("Status update")
		}
		return &types.RunResult{
			SessionID: "lean-s1",
			Output:    "Status: all good",
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	orchRunner := agent.NewMockRunner()

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, 0, 0, 0, 0, testLogger())

	cfg := config.DefaultConfig()

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "{{.issue.title}}",
		Runner:         generalRunner,
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		OrchAgent:      orchAgent,
		ChannelManager: mgr,
	})

	orch.guestIndex = &guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"writer-1": {ID: "writer-1", Name: "Story Writer", Description: "Writes stories"},
		},
	}

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind:  "test",
		SenderID:     "user-1",
		ThreadID:     "thread-systag",
		Text:         "[system:status] Show project status",
		ActiveGuests: []string{"writer-1"},
		Timestamp:    time.Now(),
	})

	if atomic.LoadInt64(&leanCalls) == 0 {
		t.Error("expected lean handler to run for system tag even with active guests, but it was not called")
	}
}

func TestHandleGuestMention_StreamsDeltas(t *testing.T) {
	dir := t.TempDir()
	agentsDir := dir + "/agents"
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentsDir+"/writer-1.md", []byte("---\nname: Writer\ndescription: writes\nrole: writer\n---\nYou write."), 0o644); err != nil {
		t.Fatal(err)
	}

	guestRunner := agent.NewMockRunner()
	guestRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if opts.OnStream != nil {
			opts.OnStream("Once ")
			opts.OnStream("upon a time.")
		}
		return &types.RunResult{Output: "Once upon a time."}, nil
	}

	orchRunner := agent.NewMockRunner()

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, 0, 0, 0, 0, testLogger())

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = dir

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "{{.issue.title}}",
		Tracker:        tracker.NewMockTracker(nil),
		Runner:         guestRunner,
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		OrchAgent:      orchAgent,
		ChannelManager: mgr,
	})

	orch.guestIndex = &guests.GuestIndex{
		Agents: map[string]guests.GuestAgent{
			"writer-1": {ID: "writer-1", Name: "Writer", Description: "writes"},
		},
	}

	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind:  "test",
		SenderID:     "user-1",
		ThreadID:     "thread-mention",
		Text:         "Tell me a story",
		ActiveGuests: []string{"writer-1"},
		Mention:      "writer-1",
		Timestamp:    time.Now(),
	})

	sent := ch.sentMessages()
	var deltas []string
	doneCount := 0
	var textMsgs []string
	for _, m := range sent {
		if m.StreamDelta != "" {
			deltas = append(deltas, m.StreamDelta)
			if m.GuestID != "writer-1" {
				t.Errorf("stream delta GuestID = %q, want writer-1", m.GuestID)
			}
			if m.ThreadID != "thread-mention" {
				t.Errorf("stream delta ThreadID = %q, want thread-mention", m.ThreadID)
			}
		}
		if m.StreamDone {
			doneCount++
			if m.GuestID != "writer-1" {
				t.Errorf("stream done GuestID = %q, want writer-1", m.GuestID)
			}
		}
		if m.Text != "" && !m.Ack {
			textMsgs = append(textMsgs, m.Text)
		}
	}

	if len(deltas) < 2 {
		t.Fatalf("expected at least 2 stream deltas, got %d: %v", len(deltas), deltas)
	}
	if doneCount != 1 {
		t.Errorf("expected 1 stream done, got %d", doneCount)
	}
	if len(textMsgs) > 0 {
		t.Errorf("expected no duplicate final Text (content was streamed), got %v", textMsgs)
	}
}
