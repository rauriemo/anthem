package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/workspace"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestOrchestratorDispatchesTasks(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "Fix bug", Body: "Fix it", Labels: []string{"todo"}, Status: types.StatusActive, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	var dispatched []string
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		dispatched = append(dispatched, opts.Prompt)
		return &types.RunResult{SessionID: "s1", ExitCode: 0, Duration: time.Millisecond}, nil
	}

	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "test/repo"
	cfg.Polling.IntervalMS = 1000

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "Work on {{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    ws,
		EventBus:     events,
		Logger:       logger,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = orch.Run(ctx)

	// Wait a bit for goroutine to complete
	time.Sleep(100 * time.Millisecond)

	if len(dispatched) == 0 {
		t.Fatal("expected at least one dispatch")
	}
	if dispatched[0] != "Work on Fix bug" {
		t.Errorf("prompt = %q, want 'Work on Fix bug'", dispatched[0])
	}
}

func TestOrchestratorRespectsMaxConcurrent(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "T1", Labels: []string{"todo"}, Status: types.StatusActive, Priority: 1, CreatedAt: time.Now()},
		{ID: "2", Identifier: "GH-2", Title: "T2", Labels: []string{"todo"}, Status: types.StatusActive, Priority: 2, CreatedAt: time.Now()},
		{ID: "3", Identifier: "GH-3", Title: "T3", Labels: []string{"todo"}, Status: types.StatusActive, Priority: 3, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	running := make(chan struct{})
	runner := agent.NewMockRunner()
	runner.RunFunc = func(ctx context.Context, _ types.RunOpts) (*types.RunResult, error) {
		running <- struct{}{}
		<-ctx.Done()
		return &types.RunResult{ExitCode: 0, Duration: time.Millisecond}, nil
	}

	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 1000
	cfg.Agent.MaxConcurrent = 2

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    ws,
		EventBus:     events,
		Logger:       logger,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() { _ = orch.Run(ctx) }()

	// Should get exactly 2 dispatches (max_concurrent = 2)
	<-running
	<-running

	// Third should not dispatch because we're at capacity
	time.Sleep(200 * time.Millisecond)
	if orch.activeCount() > 2 {
		t.Errorf("active count = %d, want <= 2", orch.activeCount())
	}
}

func TestOrchestratorSkipsApprovalRequired(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "Plan something", Labels: []string{"planning"}, Status: types.StatusActive, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	dispatched := false
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		dispatched = true
		return &types.RunResult{ExitCode: 0}, nil
	}

	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 1000
	cfg.Rules = []config.RuleConfig{
		{Match: config.RuleMatch{Labels: []string{"planning"}}, Action: "require_approval", ApprovalLabel: "approved"},
	}

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    ws,
		EventBus:     events,
		Logger:       logger,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = orch.Run(ctx)

	if dispatched {
		t.Error("task should not have been dispatched without approval")
	}

	// Check that waiting-for-approval label was added
	task, _ := trk.GetTask(context.Background(), "1")
	found := false
	for _, l := range task.Labels {
		if l == "waiting-for-approval" {
			found = true
		}
	}
	if !found {
		t.Error("expected waiting-for-approval label to be added")
	}
}

func TestTickSkippedWhenThrottled(t *testing.T) {
	tests := []struct {
		name           string
		throttleUntil  time.Time
		wantDispatched bool
	}{
		{
			name:           "tick skipped when throttled",
			throttleUntil:  time.Now().Add(10 * time.Minute),
			wantDispatched: false,
		},
		{
			name:           "tick runs after throttle expires",
			throttleUntil:  time.Now().Add(-1 * time.Second),
			wantDispatched: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tasks := []types.Task{
				{ID: "1", Identifier: "GH-1", Title: "T1", Labels: []string{"todo"}, Status: types.StatusActive, Priority: 1, CreatedAt: time.Now()},
			}
			trk := tracker.NewMockTracker(tasks)
			trk.ThrottleUntil = tt.throttleUntil

			dispatched := false
			runner := agent.NewMockRunner()
			runner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
				dispatched = true
				return &types.RunResult{ExitCode: 0, Duration: time.Millisecond}, nil
			}

			ws := workspace.NewMockWorkspaceManager()
			events := NewMockEventBus()
			logger := testLogger()

			cfg := config.DefaultConfig()
			cfg.Tracker.Kind = "github"
			cfg.Tracker.Repo = "t/r"
			cfg.Polling.IntervalMS = 1000

			orch := New(Opts{
				Config:       &cfg,
				TemplateBody: "{{.issue.title}}",
				Tracker:      trk,
				Runner:       runner,
				Workspace:    ws,
				EventBus:     events,
				Logger:       logger,
			})

			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()

			_ = orch.Run(ctx)
			time.Sleep(100 * time.Millisecond)

			if dispatched != tt.wantDispatched {
				t.Errorf("dispatched = %v, want %v", dispatched, tt.wantDispatched)
			}
		})
	}
}

func TestConstraintsInPrompt(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "Fix bug", Body: "Fix it", Labels: []string{"todo"}, Status: types.StatusActive, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	var receivedPrompt string
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		receivedPrompt = opts.Prompt
		return &types.RunResult{SessionID: "s1", ExitCode: 0, Duration: time.Millisecond}, nil
	}

	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 1000
	cfg.System.Constraints = []string{"Run tests before merging"}

	orch := New(Opts{
		Config:          &cfg,
		TemplateBody:    "Work on {{.issue.title}}",
		Tracker:         trk,
		Runner:          runner,
		Workspace:       ws,
		EventBus:        events,
		Logger:          logger,
		VoiceContent:    "# Voice\n\n## Identity\nName: TestBot",
		UserConstraints: []string{"Never force-push to main"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = orch.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	if receivedPrompt == "" {
		t.Fatal("expected prompt to be set")
	}

	// Voice should appear first
	if !strings.HasPrefix(receivedPrompt, "# Voice") {
		t.Error("prompt should start with voice content")
	}

	// Constraints header
	if !strings.Contains(receivedPrompt, "## Constraints (non-negotiable)") {
		t.Error("prompt should contain constraints header")
	}

	// User constraint
	if !strings.Contains(receivedPrompt, "Never force-push to main") {
		t.Error("prompt should contain user constraint")
	}

	// Project constraint
	if !strings.Contains(receivedPrompt, "Run tests before merging") {
		t.Error("prompt should contain project constraint")
	}

	// Meta-constraint
	if !strings.Contains(receivedPrompt, "Do not modify constraint definitions") {
		t.Error("prompt should contain meta-constraint")
	}

	// Task template at the end
	if !strings.Contains(receivedPrompt, "Work on Fix bug") {
		t.Error("prompt should contain rendered task template")
	}
}

func TestBuildConstraints(t *testing.T) {
	tests := []struct {
		name               string
		userConstraints    []string
		projectConstraints []string
		wantEmpty          bool
		wantContains       []string
	}{
		{
			name:      "both empty returns empty",
			wantEmpty: true,
		},
		{
			name:            "user constraints only",
			userConstraints: []string{"No force push"},
			wantContains:    []string{"No force push", "Constraints (non-negotiable)", "Do not modify constraint definitions"},
		},
		{
			name:               "project constraints only",
			projectConstraints: []string{"Run tests"},
			wantContains:       []string{"Run tests", "Do not modify constraint definitions"},
		},
		{
			name:               "both combined",
			userConstraints:    []string{"User rule"},
			projectConstraints: []string{"Project rule"},
			wantContains:       []string{"User rule", "Project rule", "Do not modify constraint definitions"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildConstraints(tt.userConstraints, tt.projectConstraints)
			if tt.wantEmpty {
				if result != "" {
					t.Errorf("expected empty, got %q", result)
				}
				return
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("result should contain %q", want)
				}
			}
		})
	}
}

func TestPromptWithoutVoice(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "Add feature", Body: "Do it", Labels: []string{"todo"}, Status: types.StatusActive, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	var receivedPrompt string
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		receivedPrompt = opts.Prompt
		return &types.RunResult{SessionID: "s1", ExitCode: 0, Duration: time.Millisecond}, nil
	}

	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 1000
	cfg.System.Constraints = []string{"Always run linter"}

	orch := New(Opts{
		Config:          &cfg,
		TemplateBody:    "Work on {{.issue.title}}",
		Tracker:         trk,
		Runner:          runner,
		Workspace:       ws,
		EventBus:        events,
		Logger:          logger,
		VoiceContent:    "",
		UserConstraints: []string{"No force push"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = orch.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	if receivedPrompt == "" {
		t.Fatal("expected prompt to be set")
	}

	if strings.HasPrefix(receivedPrompt, "# Voice") {
		t.Error("prompt should not start with voice when VoiceContent is empty")
	}

	if !strings.HasPrefix(receivedPrompt, "## Constraints") {
		t.Error("prompt should start with constraints when voice is empty")
	}

	if !strings.Contains(receivedPrompt, "No force push") {
		t.Error("prompt should contain user constraint")
	}

	if !strings.Contains(receivedPrompt, "Always run linter") {
		t.Error("prompt should contain project constraint")
	}

	if !strings.Contains(receivedPrompt, "Work on Add feature") {
		t.Error("prompt should contain rendered task template")
	}
}

func TestSortTasks(t *testing.T) {
	now := time.Now()
	tasks := []types.Task{
		{ID: "3", Priority: 3, CreatedAt: now},
		{ID: "1", Priority: 1, CreatedAt: now.Add(-time.Hour)},
		{ID: "2", Priority: 1, CreatedAt: now},
		{ID: "4", Priority: 2, CreatedAt: now},
	}

	sortTasks(tasks)

	wantOrder := []string{"1", "2", "4", "3"}
	for i, want := range wantOrder {
		if tasks[i].ID != want {
			t.Errorf("tasks[%d].ID = %q, want %q", i, tasks[i].ID, want)
		}
	}
}
