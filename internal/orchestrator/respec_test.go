package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/workspace"
)

func TestParseRespecTarget(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bare respec", "[system:respec] Start respec", ""},
		{"named agent", "[system:respec:miyazaki] Start respec for miyazaki", "miyazaki"},
		{"cancel", "[system:respec:cancel] Cancel respec", "cancel"},
		{"with spaces", "[system:respec: tolkien ] Start", "tolkien"},
		{"no tag", "just a normal message", ""},
		{"empty target", "[system:respec:] Start", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRespecTarget(tt.input)
			if got != tt.want {
				t.Errorf("parseRespecTarget(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildRespecSystemPrompt_OrchestratorNew(t *testing.T) {
	prompt := buildRespecSystemPrompt("orchestrator", "", nil, true, "")
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !containsAll(prompt, "orchestrator", "Phase 1", "Phase 2", "Phase 3", "Phase 4", "Phase 5") {
		t.Error("prompt should contain all phases")
	}
	if containsAll(prompt, "Current Agent State") {
		t.Error("new agent prompt should not show current state")
	}
}

func TestBuildRespecSystemPrompt_OrchestratorRespec(t *testing.T) {
	fm := map[string]any{"name": "TestOrch", "role": "orchestrator"}
	prompt := buildRespecSystemPrompt("orchestrator", "## Identity\nExisting identity", fm, false, "User prefers concise")
	if !containsAll(prompt, "Current Agent State", "Existing identity", "TestOrch", "User Context", "User prefers concise") {
		t.Error("re-spec prompt should include current state and user context")
	}
}

func TestBuildRespecSystemPrompt_GuestNew(t *testing.T) {
	prompt := buildRespecSystemPrompt("miyazaki", "", nil, true, "")
	if !containsAll(prompt, "miyazaki", "specialist agent", "do NOT own") {
		t.Error("guest prompt should mention agent name and scope ownership")
	}
}

func TestBuildRespecSystemPrompt_GuestRespec(t *testing.T) {
	fm := map[string]any{"name": "Miyazaki", "role": "artist"}
	prompt := buildRespecSystemPrompt("miyazaki", "## Identity\nGame artist", fm, false, "")
	if !containsAll(prompt, "Current Agent State", "Game artist", "Miyazaki") {
		t.Error("guest re-spec prompt should include current state")
	}
}

func TestBuildRespecSystemPrompt_PhaseInstructions(t *testing.T) {
	prompt := buildRespecSystemPrompt("orchestrator", "", nil, true, "")
	if !containsAll(prompt, "update_agent_meta", "update_voice", "agent_file", "Respec complete") {
		t.Error("prompt should contain action format instructions and completion signal")
	}
}

func TestBuildRespecSystemPrompt_WritePerPhase(t *testing.T) {
	prompt := buildRespecSystemPrompt("orchestrator", "", nil, true, "")
	if !containsAll(prompt, "immediately emit JSON actions", "Do not wait until the end") {
		t.Error("prompt should instruct write-per-phase behavior")
	}
}

func TestDetectMode_Respec(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "test",
		Tracker:      newNoopTracker(),
		Runner:       newNoopRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	tests := []struct {
		input string
		want  string
	}{
		{"[system:respec] Start", "respec"},
		{"[system:respec:miyazaki] Start", "respec"},
		{"[system:respec:cancel] Cancel", "respec"},
		{"[system:plan] Make a plan", "plan"},
		{"[system:build] Build it", "build"},
		{"[system:fast] Quick", "fast"},
		{"Just a message", "agent"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := orch.detectMode(tt.input)
			if got != tt.want {
				t.Errorf("detectMode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestHandleRespecMessage_ExplicitOrchestrator(t *testing.T) {
	tmp := t.TempDir()

	ch := &testChannel{in: make(chan channel.IncomingMessage, 16)}
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchRunner := newNoopRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "respec-sess",
			ExitCode:  0,
			Output:    `{"reasoning": "starting orchestrator respec", "actions": []}`,
		}, nil
	}
	orchAgent := NewOrchestratorAgent(orchRunner, "", "", 100000, 0, 0, 0, 0, testLogger())

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = tmp

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "",
		Tracker:        newNoopTracker(),
		Runner:         newNoopRunner(),
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		ChannelManager: mgr,
		OrchAgent:      orchAgent,
	})

	orch.handleRespecMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		SenderID:    "user-1",
		Text:        "[system:respec:orchestrator] Start respec for orchestrator",
	}, "")

	msgs := ch.sentMessages()
	for _, m := range msgs {
		if strings.Contains(m.Text, "not found") || strings.Contains(m.Text, "No agents/") {
			t.Errorf("explicit /respec orchestrator should work without agents/ dir, got: %s", m.Text)
		}
	}
}

func TestHandleRespecMessage_NewAgentFile(t *testing.T) {
	tmp := t.TempDir()
	agentsDir := filepath.Join(tmp, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ch := &testChannel{in: make(chan channel.IncomingMessage, 16)}
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchRunner := newNoopRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if !strings.Contains(opts.Prompt, "newagent") {
			t.Errorf("prompt should reference agent name 'newagent', got prompt length %d", len(opts.Prompt))
		}
		return &types.RunResult{
			SessionID: "respec-new",
			ExitCode:  0,
			Output:    `{"reasoning": "creating new agent", "actions": []}`,
		}, nil
	}
	orchAgent := NewOrchestratorAgent(orchRunner, "", "", 100000, 0, 0, 0, 0, testLogger())

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = tmp

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "",
		Tracker:        newNoopTracker(),
		Runner:         newNoopRunner(),
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		ChannelManager: mgr,
		OrchAgent:      orchAgent,
	})

	orch.handleRespecMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		SenderID:    "user-1",
		Text:        "[system:respec:newagent] Start respec for newagent",
	}, "")

	msgs := ch.sentMessages()
	for _, m := range msgs {
		if strings.Contains(m.Text, "not found") || strings.Contains(m.Text, "No agents/") {
			t.Errorf("new agent respec should proceed when agents/ dir exists, got: %s", m.Text)
		}
	}
}

func TestHandleRespecMessage_ExistingAgentFile(t *testing.T) {
	tmp := t.TempDir()
	agentsDir := filepath.Join(tmp, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "miyazaki.md"), []byte("---\nname: Miyazaki\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ch := &testChannel{in: make(chan channel.IncomingMessage, 16)}
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	orchRunner := newNoopRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "respec-sess",
			ExitCode:  0,
			Output:    `{"reasoning": "starting respec", "actions": []}`,
		}, nil
	}
	orchAgent := NewOrchestratorAgent(orchRunner, "", "", 100000, 0, 0, 0, 0, testLogger())

	cfg := config.DefaultConfig()
	cfg.Workspace.Root = tmp

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "",
		Tracker:        newNoopTracker(),
		Runner:         newNoopRunner(),
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		ChannelManager: mgr,
		OrchAgent:      orchAgent,
	})

	orch.handleRespecMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "test",
		SenderID:    "user-1",
		Text:        "[system:respec:miyazaki] Start respec for miyazaki",
	}, "")

	msgs := ch.sentMessages()
	for _, m := range msgs {
		if strings.Contains(m.Text, "Agent file not found") {
			t.Error("should not get 'Agent file not found' error for existing agent")
		}
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
