package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/cost"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/workspace"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestOrchestratorDispatchesTasks(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "Fix bug", Body: "Fix it", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
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
		{ID: "1", Identifier: "GH-1", Title: "T1", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
		{ID: "2", Identifier: "GH-2", Title: "T2", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 2, CreatedAt: time.Now()},
		{ID: "3", Identifier: "GH-3", Title: "T3", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 3, CreatedAt: time.Now()},
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
		{ID: "1", Identifier: "GH-1", Title: "Plan something", Labels: []string{"planning"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
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
				{ID: "1", Identifier: "GH-1", Title: "T1", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
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
		{ID: "1", Identifier: "GH-1", Title: "Fix bug", Body: "Fix it", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
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

	// Voice should NOT appear in executor prompts (orchestrator-only)
	if strings.Contains(receivedPrompt, "# Voice") {
		t.Error("executor prompt should not contain voice content")
	}

	// Constraints should appear first
	if !strings.HasPrefix(receivedPrompt, "## Constraints") {
		t.Error("prompt should start with constraints")
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
		{ID: "1", Identifier: "GH-1", Title: "Add feature", Body: "Do it", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
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

func TestMaxCostEnforcementSkipsOverBudget(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "Expensive task", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	dispatched := false
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		dispatched = true
		return &types.RunResult{ExitCode: 0, Duration: time.Millisecond}, nil
	}

	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	ct := cost.NewTracker()
	// Pre-record cost that exceeds the budget
	ct.Record(cost.SessionCost{TaskID: "1", SessionID: "prev", CostUSD: 6.0})

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 1000
	cfg.Rules = []config.RuleConfig{
		{Match: config.RuleMatch{Labels: []string{"todo"}}, Action: "max_cost", MaxCost: 5.0},
	}

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    ws,
		EventBus:     events,
		CostTracker:  ct,
		Logger:       logger,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = orch.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	if dispatched {
		t.Error("task should not have been dispatched when over budget")
	}

	// Check exceeded-budget label was added
	task, _ := trk.GetTask(context.Background(), "1")
	found := false
	for _, l := range task.Labels {
		if l == "exceeded-budget" {
			found = true
		}
	}
	if !found {
		t.Error("expected exceeded-budget label to be added")
	}

	// Check budget_exceeded event was published
	events.mu.Lock()
	var budgetEvent bool
	for _, e := range events.Published {
		if e.Type == "task.budget_exceeded" {
			budgetEvent = true
		}
	}
	events.mu.Unlock()
	if !budgetEvent {
		t.Error("expected task.budget_exceeded event")
	}
}

func TestMaxCostAllowsUnderBudget(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "Cheap task", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	dispatched := false
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		dispatched = true
		return &types.RunResult{SessionID: "s1", ExitCode: 0, CostUSD: 1.0, Duration: time.Millisecond}, nil
	}

	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	ct := cost.NewTracker()
	// Pre-record some cost, but under budget
	ct.Record(cost.SessionCost{TaskID: "1", SessionID: "prev", CostUSD: 2.0})

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 1000
	cfg.Rules = []config.RuleConfig{
		{Match: config.RuleMatch{Labels: []string{"todo"}}, Action: "max_cost", MaxCost: 5.0},
	}

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    ws,
		EventBus:     events,
		CostTracker:  ct,
		Logger:       logger,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = orch.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	if !dispatched {
		t.Error("task should have been dispatched when under budget")
	}
}

func TestAutoAssignAddsComment(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "Fix bug", Body: "Fix it", Labels: []string{"bug"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{SessionID: "s1", ExitCode: 0, Duration: time.Millisecond}, nil
	}

	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 1000
	cfg.Rules = []config.RuleConfig{
		{Match: config.RuleMatch{Labels: []string{"bug"}}, Action: "auto_assign", AutoAssignee: "alice"},
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
	time.Sleep(100 * time.Millisecond)

	// Access after goroutine completion (time.Sleep above)
	comments := trk.Comments["1"]

	found := false
	for _, c := range comments {
		if strings.Contains(c, "Auto-assigned to @alice") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected auto-assign comment, got comments: %v", comments)
	}
}

func TestComputeBackoff(t *testing.T) {
	tests := []struct {
		name         string
		attempts     int
		maxBackoffMS int
		wantDuration time.Duration
	}{
		{
			name:         "attempt 1 = 10s",
			attempts:     1,
			maxBackoffMS: 300000,
			wantDuration: 10 * time.Second,
		},
		{
			name:         "attempt 2 = 20s",
			attempts:     2,
			maxBackoffMS: 300000,
			wantDuration: 20 * time.Second,
		},
		{
			name:         "attempt 3 = 40s",
			attempts:     3,
			maxBackoffMS: 300000,
			wantDuration: 40 * time.Second,
		},
		{
			name:         "attempt 4 = 80s",
			attempts:     4,
			maxBackoffMS: 300000,
			wantDuration: 80 * time.Second,
		},
		{
			name:         "attempt 5 = 160s",
			attempts:     5,
			maxBackoffMS: 300000,
			wantDuration: 160 * time.Second,
		},
		{
			name:         "capped at maxBackoffMS",
			attempts:     10,
			maxBackoffMS: 300000,
			wantDuration: 300 * time.Second,
		},
		{
			name:         "small cap",
			attempts:     3,
			maxBackoffMS: 15000,
			wantDuration: 15 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeBackoff(tt.attempts, tt.maxBackoffMS)
			if got != tt.wantDuration {
				t.Errorf("computeBackoff(%d, %d) = %v, want %v", tt.attempts, tt.maxBackoffMS, got, tt.wantDuration)
			}
		})
	}
}

func TestFailedTaskNotRedispatchedBeforeRetryTime(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "Flaky task", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	dispatchCount := 0
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		dispatchCount++
		return nil, errors.New("agent crashed")
	}

	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 200
	cfg.Agent.MaxRetryBackoffMS = 300000

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    ws,
		EventBus:     events,
		Logger:       logger,
	})

	// Run for 1.5s — with 200ms polling, that's ~7 ticks.
	// First tick dispatches and fails. Backoff is 10s, so subsequent ticks should skip.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	_ = orch.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	if dispatchCount != 1 {
		t.Errorf("dispatchCount = %d, want 1 (task should not be re-dispatched during backoff)", dispatchCount)
	}

	// Verify retry comment was posted
	comments := trk.Comments["1"]
	found := false
	for _, c := range comments {
		if strings.Contains(c, "Retry attempt 1") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected retry comment, got: %v", comments)
	}
}

func TestSuccessfulCompletionClearsRetryState(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "Recovering task", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{SessionID: "s1", ExitCode: 0, Duration: time.Millisecond}, nil
	}

	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 1000
	cfg.Agent.MaxRetryBackoffMS = 300000

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    ws,
		EventBus:     events,
		Logger:       logger,
	})

	// Pre-seed retry state as if task had previously failed
	orch.mu.Lock()
	orch.retryState["1"] = &RetryInfo{
		TaskID:      "1",
		Attempts:    2,
		NextRetryAt: time.Now().Add(-1 * time.Second), // eligible now
		LastError:   "previous error",
	}
	orch.mu.Unlock()

	// 2.5s timeout: tick dispatches immediately, 1s continuation delay, then agent runs
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()

	_ = orch.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	orch.mu.Lock()
	_, hasRetry := orch.retryState["1"]
	orch.mu.Unlock()

	if hasRetry {
		t.Error("retry state should be cleared after successful completion")
	}
}

func TestReconcileReleasesStaleClaimBeyondStallTimeout(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "Stalled task", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	runner := agent.NewMockRunner()
	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 10000
	cfg.Agent.StallTimeoutMS = 100 // 100ms for testing

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    ws,
		EventBus:     events,
		Logger:       logger,
	})

	// Manually add an active run that started long ago (beyond 2x stall timeout)
	orch.mu.Lock()
	orch.active["1"] = &ActiveRun{
		TaskID:    "1",
		Labels:    []string{"todo"},
		StartedAt: time.Now().Add(-1 * time.Second), // well beyond 2*100ms = 200ms
	}
	orch.mu.Unlock()

	// Run reconcile
	orch.reconcile(context.Background())

	// Active run should have been released
	if orch.activeCount() != 0 {
		t.Errorf("active count = %d, want 0 (stalled claim should be released)", orch.activeCount())
	}

	// Check stalled event was published
	events.mu.Lock()
	var stalledEvent bool
	for _, e := range events.Published {
		if e.Type == "task.stalled" {
			stalledEvent = true
		}
	}
	events.mu.Unlock()
	if !stalledEvent {
		t.Error("expected task.stalled event")
	}
}

func TestShutdownWaitsForActiveDispatches(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "Slow task", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	dispatchCompleted := make(chan struct{})
	runner := agent.NewMockRunner()
	runner.RunFunc = func(ctx context.Context, _ types.RunOpts) (*types.RunResult, error) {
		// Simulate slow agent work (finishes before drain timeout)
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
		}
		close(dispatchCompleted)
		return &types.RunResult{SessionID: "s1", ExitCode: 0, Duration: 500 * time.Millisecond}, nil
	}

	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 100000 // won't tick again

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    ws,
		EventBus:     events,
		Logger:       logger,
	})

	ctx, cancel := context.WithCancel(context.Background())

	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	// Wait for dispatch to start
	time.Sleep(200 * time.Millisecond)

	// Cancel to trigger shutdown
	cancel()

	// Run() should wait for the dispatch to finish before returning
	select {
	case <-runDone:
		// Verify the dispatch actually completed
		select {
		case <-dispatchCompleted:
			// Good — dispatch finished before Run returned
		default:
			t.Error("Run() returned before dispatch completed")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run() did not return within 15s")
	}
}

func TestShutdownTimeoutOnStuckDispatch(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "Stuck task", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	runner := agent.NewMockRunner()
	runner.RunFunc = func(ctx context.Context, _ types.RunOpts) (*types.RunResult, error) {
		// Never finishes — blocks until test times out
		<-ctx.Done()
		// Simulate a stuck agent that doesn't respond to cancellation quickly
		time.Sleep(30 * time.Second)
		return &types.RunResult{ExitCode: 1}, nil
	}

	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 100000

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    ws,
		EventBus:     events,
		Logger:       logger,
	})

	ctx, cancel := context.WithCancel(context.Background())

	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	// Wait for dispatch to start
	time.Sleep(200 * time.Millisecond)
	cancel()

	// Shutdown should time out after ~10s, not hang forever
	start := time.Now()
	select {
	case <-runDone:
		elapsed := time.Since(start)
		if elapsed < 9*time.Second {
			t.Errorf("shutdown returned too quickly (%v), expected ~10s drain timeout", elapsed)
		}
		if elapsed > 15*time.Second {
			t.Errorf("shutdown took too long (%v), expected ~10s drain timeout", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run() did not return within 20s — shutdown drain timeout not working")
	}
}

func TestShutdownRemovesInProgressLabels(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "T1", Labels: []string{"todo", "in-progress"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
		{ID: "2", Identifier: "GH-2", Title: "T2", Labels: []string{"todo", "in-progress"}, Status: types.StatusQueued, Priority: 2, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	runner := agent.NewMockRunner()
	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 100000

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    ws,
		EventBus:     events,
		Logger:       logger,
	})

	// Manually seed active runs (simulating in-progress dispatches that already finished)
	orch.mu.Lock()
	orch.active["1"] = &ActiveRun{TaskID: "1", Labels: []string{"todo"}, StartedAt: time.Now()}
	orch.active["2"] = &ActiveRun{TaskID: "2", Labels: []string{"todo"}, StartedAt: time.Now()}
	orch.mu.Unlock()

	orch.Shutdown()

	// Active map should be empty
	if orch.activeCount() != 0 {
		t.Errorf("active count = %d, want 0 after shutdown", orch.activeCount())
	}

	// in-progress label should have been removed from both tasks
	for _, id := range []string{"1", "2"} {
		task, _ := trk.GetTask(context.Background(), id)
		for _, l := range task.Labels {
			if l == "in-progress" {
				t.Errorf("task %s still has in-progress label after shutdown", id)
			}
		}
	}
}

func TestReconcileDoesNotReleaseRecentRun(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "Active task", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	runner := agent.NewMockRunner()
	ws := workspace.NewMockWorkspaceManager()
	events := NewMockEventBus()
	logger := testLogger()

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 10000
	cfg.Agent.StallTimeoutMS = 300000 // 5 minutes

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    ws,
		EventBus:     events,
		Logger:       logger,
	})

	// Add a recently started run
	orch.mu.Lock()
	orch.active["1"] = &ActiveRun{
		TaskID:    "1",
		Labels:    []string{"todo"},
		StartedAt: time.Now(),
	}
	orch.mu.Unlock()

	orch.reconcile(context.Background())

	// Should still be active
	if orch.activeCount() != 1 {
		t.Errorf("active count = %d, want 1 (recent run should not be released)", orch.activeCount())
	}
}
