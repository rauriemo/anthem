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

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, 10, testLogger())

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
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if !strings.Contains(opts.Prompt, "PLANNING mode") {
			t.Error("expected plan mode prompt suffix")
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
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
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

func TestHandleBuildMessage_CreatesSubtasks(t *testing.T) {
	orchRunner := agent.NewMockRunner()

	// First call is from Save (plan creation), second from build
	callCount := 0
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		callCount++
		if strings.Contains(opts.Prompt, "Build Mode") {
			return &types.RunResult{
				SessionID: "build-s1",
				Output: `{"reasoning": "Building from approved plan", "actions": [
					{"type": "create_subtasks", "subtasks": [{"title": "Task A", "body": "Do A", "labels": ["priority:high"]}]},
					{"type": "reply", "body": "Created 1 task from your plan."}
				]}`,
				TokensIn: 100, TokensOut: 80,
			}, nil
		}
		// Plan mode call
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    "```anthem-plan\n# Build Test\n\n## Tasks\n\n### 1. Task A\n```",
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	orch, ch := newPlanTestOrch(t, orchRunner)

	// First create a plan
	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] Build something",
		Timestamp:   time.Now(),
	})

	// Get the saved plan path
	metas, _ := orch.planStore.List("owner/repo")
	if len(metas) == 0 {
		t.Fatal("expected plan to exist")
	}

	// Now build it
	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t2",
		Text:        "[system:build] " + metas[0].Path,
		Timestamp:   time.Now(),
	})

	// Check plan status changed to done
	plan, err := orch.planStore.Load(metas[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Frontmatter.Status != plans.StatusDone {
		t.Errorf("plan status = %q, want done", plan.Frontmatter.Status)
	}

	// Verify a reply was sent
	sent := ch.sentMessages()
	var foundReply bool
	for _, msg := range sent {
		if msg.Text != "" && strings.Contains(msg.Text, "Created 1 task") {
			foundReply = true
		}
	}
	if !foundReply {
		t.Error("expected build confirmation reply")
	}

	// Verify the created task has "todo" auto-added (LLM only provided "priority:high")
	trk := orch.tracker.(*tracker.MockTracker)
	createdTask, _ := trk.GetTask(context.Background(), "2")
	if createdTask == nil {
		t.Fatal("expected task with ID '2' to be created")
	}
	if createdTask.Title != "Task A" {
		t.Errorf("created task title = %q, want 'Task A'", createdTask.Title)
	}
	hasTodo := false
	hasPriority := false
	for _, l := range createdTask.Labels {
		if l == "todo" {
			hasTodo = true
		}
		if l == "priority:high" {
			hasPriority = true
		}
	}
	if !hasTodo {
		t.Errorf("built task labels = %v, want 'todo' to be auto-added", createdTask.Labels)
	}
	if !hasPriority {
		t.Errorf("built task labels = %v, want 'priority:high' to be preserved", createdTask.Labels)
	}
}

func TestHandleBuildMessage_NoTracker_FallsBackToDraft(t *testing.T) {
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		if strings.Contains(opts.Prompt, "Build Mode") {
			return &types.RunResult{
				SessionID: "build-s1",
				Output:    `{"reasoning": "building", "actions": [{"type": "reply", "body": "Done."}]}`,
				TokensIn:  10, TokensOut: 5,
			}, nil
		}
		return &types.RunResult{
			SessionID: "plan-s1",
			Output:    "```anthem-plan\n# Fallback Test\n```",
			TokensIn:  10, TokensOut: 5,
		}, nil
	}

	orch, _ := newPlanTestOrch(t, orchRunner)

	// Create a plan first
	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t1",
		Text:        "[system:plan] test",
		Timestamp:   time.Now(),
	})

	// Build without specifying path -- should find latest draft
	orch.HandleUserMessage(context.Background(), channel.IncomingMessage{
		ChannelKind: "prism",
		SenderID:    "user-1",
		ThreadID:    "t2",
		Text:        "[system:build]",
		Timestamp:   time.Now(),
	})

	metas, _ := orch.planStore.List("owner/repo")
	if len(metas) == 0 {
		t.Fatal("expected plan to exist")
	}
	plan, _ := orch.planStore.Load(metas[0].Path)
	if plan.Frontmatter.Status != plans.StatusDone {
		t.Errorf("plan status = %q, want done", plan.Frontmatter.Status)
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

func TestPlanModePromptContents(t *testing.T) {
	checks := []string{
		"PLANNING mode",
		"Do NOT output JSON actions",
		"anthem-plan",
		"create issues",
		"todo",
	}
	for _, check := range checks {
		if !strings.Contains(planModePromptSuffix, check) {
			t.Errorf("planModePromptSuffix missing %q", check)
		}
	}
}

func TestBuildModePromptContents(t *testing.T) {
	checks := []string{
		"Build Mode",
		"create_subtasks",
		"approved the plan",
		"1-based ordinal",
		"todo",
	}
	for _, check := range checks {
		if !strings.Contains(buildModePromptSuffix, check) {
			t.Errorf("buildModePromptSuffix missing %q", check)
		}
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
