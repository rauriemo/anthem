package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/workspace"
)

func TestTick_MechanicalFallback(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "T1", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	dispatched := false
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		dispatched = true
		return &types.RunResult{SessionID: "s1", ExitCode: 0, Duration: time.Millisecond}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 100000

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
		OrchAgent:    nil, // no orchestrator agent — should use mechanical dispatch
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = orch.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	if !dispatched {
		t.Error("expected mechanical fallback to dispatch the task")
	}
}

func TestTick_DirtySnapshotGating(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "T1", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	consultCalls := 0
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		consultCalls++
		return &types.RunResult{
			SessionID: "orch-s1",
			ExitCode:  0,
			Output:    `{"reasoning": "dispatch", "actions": [{"type": "dispatch", "task_id": "1"}]}`,
			TokensIn:  10,
			TokensOut: 5,
			Duration:  time.Millisecond,
		}, nil
	}

	execRunner := agent.NewMockRunner()
	execRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{SessionID: "exec-s1", ExitCode: 0, Duration: time.Millisecond}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 100

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, testLogger())

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       execRunner,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
		OrchAgent:    orchAgent,
	})

	// First tick — should consult orchestrator
	orch.tick(context.Background())
	firstCalls := consultCalls

	// Wait for dispatch goroutine to complete
	time.Sleep(100 * time.Millisecond)

	// Second tick with same state — snapshot hash unchanged, should skip
	orch.tick(context.Background())

	if consultCalls != firstCalls {
		t.Errorf("orchestrator consulted %d times on unchanged snapshot, expected %d", consultCalls, firstCalls)
	}
}

func TestWaveExhaustion(t *testing.T) {
	trk := tracker.NewMockTracker([]types.Task{
		{ID: "1", Status: types.StatusCompleted},
		{ID: "2", Status: types.StatusFailed},
	})

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       agent.NewMockRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	orch.currentWave = &Wave{
		ID:              "wave-test",
		FrontierTaskIDs: []string{"1", "2"},
		Status:          "active",
		CreatedAt:       time.Now(),
	}

	if !orch.isWaveExhausted() {
		t.Error("expected wave to be exhausted when all frontier tasks are terminal")
	}

	// Add a non-terminal task
	trk2 := tracker.NewMockTracker([]types.Task{
		{ID: "1", Status: types.StatusCompleted},
		{ID: "3", Status: types.StatusRunning},
	})
	orch.tracker = trk2
	orch.currentWave.FrontierTaskIDs = []string{"1", "3"}

	if orch.isWaveExhausted() {
		t.Error("expected wave NOT to be exhausted with running frontier task")
	}
}

func TestTick_OrchestratorFallback(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "T1", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	fallbackDispatched := false
	execRunner := agent.NewMockRunner()
	execRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		fallbackDispatched = true
		return &types.RunResult{SessionID: "s1", ExitCode: 0, Duration: time.Millisecond}, nil
	}

	// Orchestrator agent that always returns invalid output (triggers fallback)
	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "orch-fail",
			ExitCode:  0,
			Output:    "not json at all",
			TokensIn:  10,
			TokensOut: 5,
			Duration:  time.Millisecond,
		}, nil
	}
	orchRunner.ContinueFunc = func(_ context.Context, _ string, _ string, _ types.ContinueOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "orch-fail",
			ExitCode:  0,
			Output:    "still not json",
			TokensIn:  10,
			TokensOut: 5,
			Duration:  time.Millisecond,
		}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 100000

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, testLogger())

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       execRunner,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
		OrchAgent:    orchAgent,
	})

	orch.tick(context.Background())
	time.Sleep(200 * time.Millisecond)

	if !fallbackDispatched {
		t.Error("expected fallback mechanical dispatch when orchestrator fails")
	}
}
