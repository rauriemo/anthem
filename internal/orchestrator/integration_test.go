package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/audit"
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/cost"
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

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, 0, 0, 0, 0, testLogger())

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

func TestTick_FiltersReplyAndDisplayFromPollingCycle(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "T1", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	orchRunner := agent.NewMockRunner()
	orchRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{
			SessionID: "orch-s1",
			Output: `{"reasoning": "greet and show", "actions": [
				{"type": "dispatch", "task_id": "1"},
				{"type": "reply", "body": "Hello from polling"},
				{"type": "display", "display_kind": "html", "display_content": "<p>hi</p>"}
			]}`,
			TokensIn: 10, TokensOut: 5,
			Duration: time.Millisecond,
		}, nil
	}

	execRunner := agent.NewMockRunner()
	execRunner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{SessionID: "exec-s1", ExitCode: 0, Duration: time.Millisecond}, nil
	}

	ch := newTestChannel()
	mgr := channel.NewManager(nil)
	mgr.Register(ch)

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 100

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, 0, 0, 0, 0, testLogger())

	orch := New(Opts{
		Config:         &cfg,
		TemplateBody:   "{{.issue.title}}",
		Tracker:        trk,
		Runner:         execRunner,
		Workspace:      workspace.NewMockWorkspaceManager(),
		EventBus:       NewMockEventBus(),
		Logger:         testLogger(),
		OrchAgent:      orchAgent,
		ChannelManager: mgr,
	})

	orch.tick(context.Background())
	time.Sleep(100 * time.Millisecond)

	sent := ch.sentMessages()
	for _, msg := range sent {
		if msg.Text == "Hello from polling" {
			t.Error("reply action from polling cycle should have been filtered out")
		}
		if msg.Display != nil {
			t.Error("display action from polling cycle should have been filtered out")
		}
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

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, 0, 0, 0, 0, testLogger())

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

func TestExecuteActions_CreateSubtasks(t *testing.T) {
	trk := tracker.NewMockTracker([]types.Task{
		{ID: "1", Title: "Parent", Status: types.StatusQueued},
	})

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Tracker.Labels.Active = []string{"todo"}

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       newNoopRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	tasks := []types.Task{{ID: "1", Title: "Parent", Status: types.StatusQueued}}
	actions := []Action{
		{
			Type: ActionCreateSubtasks,
			Subtasks: []SubtaskDef{
				{Title: "Subtask A", Body: "Do A", Labels: []string{"priority:high"}},
				{Title: "Subtask B", Body: "Do B", Labels: []string{"todo", "bug"}},
			},
		},
	}

	orch.executeActions(context.Background(), tasks, actions)

	if len(trk.Tasks) != 3 {
		t.Fatalf("expected 3 tasks (1 original + 2 subtasks), got %d", len(trk.Tasks))
	}
	if trk.Tasks[1].Title != "Subtask A" {
		t.Errorf("subtask 1 title = %q, want 'Subtask A'", trk.Tasks[1].Title)
	}
	// Subtask A had no "todo" — auto-add should have added it via AddLabel
	if !slices.Contains(trk.Tasks[1].Labels, "todo") {
		t.Errorf("subtask A labels = %v, want 'todo' to be auto-added", trk.Tasks[1].Labels)
	}
	// Subtask B already had "todo" — should not be duplicated
	if trk.Tasks[2].Title != "Subtask B" {
		t.Errorf("subtask 2 title = %q, want 'Subtask B'", trk.Tasks[2].Title)
	}
	todoCount := 0
	for _, l := range trk.Tasks[2].Labels {
		if l == "todo" {
			todoCount++
		}
	}
	if todoCount != 1 {
		t.Errorf("subtask B has %d 'todo' labels, want exactly 1; labels = %v", todoCount, trk.Tasks[2].Labels)
	}
}

func TestDispatch_AuditRecordOnCompletion(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "T1", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{SessionID: "s1", ExitCode: 0, CostUSD: 0.42, Duration: time.Millisecond}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 100000
	eb := NewMockEventBus()

	// Use a shared audit DB path — Shutdown closes it, so we reopen to query
	dbPath := filepath.Join(t.TempDir(), "audit-completion.db")
	auditLog, err := audit.NewSQLiteAuditLogger(dbPath)
	if err != nil {
		t.Fatalf("creating audit logger: %v", err)
	}

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     eb,
		Logger:       testLogger(),
		AuditLogger:  auditLog,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = orch.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	// Reopen DB to query (Shutdown closed it)
	auditLog2, err := audit.NewSQLiteAuditLogger(dbPath)
	if err != nil {
		t.Fatalf("reopening audit logger: %v", err)
	}
	defer auditLog2.Close()

	events, err := auditLog2.Query(context.Background(), audit.QueryFilter{EventType: "task.completed"})
	if err != nil {
		t.Fatalf("querying audit: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected task.completed audit record")
	}
	ev := events[0]
	if ev.TaskID == nil || *ev.TaskID != "1" {
		t.Errorf("task_id = %v, want '1'", ev.TaskID)
	}
	if ev.CostUSD == nil || *ev.CostUSD != 0.42 {
		t.Errorf("cost_usd = %v, want 0.42", ev.CostUSD)
	}
}

func TestDispatch_AuditRecordOnFailure(t *testing.T) {
	tasks := []types.Task{
		{ID: "2", Identifier: "GH-2", Title: "Failing", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return nil, errors.New("boom")
	}

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 100000

	dbPath := filepath.Join(t.TempDir(), "audit-failure.db")
	auditLog, err := audit.NewSQLiteAuditLogger(dbPath)
	if err != nil {
		t.Fatalf("creating audit logger: %v", err)
	}

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
		AuditLogger:  auditLog,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = orch.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	auditLog2, err := audit.NewSQLiteAuditLogger(dbPath)
	if err != nil {
		t.Fatalf("reopening audit logger: %v", err)
	}
	defer auditLog2.Close()

	events, err := auditLog2.Query(context.Background(), audit.QueryFilter{EventType: "task.failed"})
	if err != nil {
		t.Fatalf("querying audit: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected task.failed audit record")
	}
	ev := events[0]
	if ev.TaskID == nil || *ev.TaskID != "2" {
		t.Errorf("task_id = %v, want '2'", ev.TaskID)
	}
	if ev.Error == nil || *ev.Error != "boom" {
		t.Errorf("error = %v, want 'boom'", ev.Error)
	}
}

func TestDispatch_AuditRecordOnNonZeroExit(t *testing.T) {
	tasks := []types.Task{
		{ID: "3", Identifier: "GH-3", Title: "BadExit", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{SessionID: "s1", ExitCode: 1, CostUSD: 0.10}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 100000

	dbPath := filepath.Join(t.TempDir(), "audit-nzexit.db")
	auditLog, err := audit.NewSQLiteAuditLogger(dbPath)
	if err != nil {
		t.Fatalf("creating audit logger: %v", err)
	}

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
		AuditLogger:  auditLog,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = orch.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	auditLog2, err := audit.NewSQLiteAuditLogger(dbPath)
	if err != nil {
		t.Fatalf("reopening audit logger: %v", err)
	}
	defer auditLog2.Close()

	events, err := auditLog2.Query(context.Background(), audit.QueryFilter{EventType: "task.failed"})
	if err != nil {
		t.Fatalf("querying audit: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected task.failed audit record for non-zero exit")
	}
	ev := events[0]
	if ev.CostUSD == nil || *ev.CostUSD != 0.10 {
		t.Errorf("cost_usd = %v, want 0.10", ev.CostUSD)
	}
}

func TestBuildStateSnapshot_WaveSpentUSD(t *testing.T) {
	cfg := config.DefaultConfig()
	ct := cost.NewTracker()
	ct.Record(cost.SessionCost{TaskID: "t1", SessionID: "s1", CostUSD: 0.50})
	ct.Record(cost.SessionCost{TaskID: "t2", SessionID: "s2", CostUSD: 0.30})
	ct.Record(cost.SessionCost{TaskID: "t3", SessionID: "s3", CostUSD: 1.00})

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "",
		Tracker:      tracker.NewMockTracker(nil),
		Runner:       newNoopRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
		CostTracker:  ct,
	})

	orch.currentWave = &Wave{
		ID:              "wave-1",
		FrontierTaskIDs: []string{"t1", "t2"},
		Status:          "active",
	}

	tasks := []types.Task{
		{ID: "t1", Status: types.StatusRunning},
		{ID: "t2", Status: types.StatusRunning},
		{ID: "t3", Status: types.StatusCompleted},
	}

	snap := orch.buildStateSnapshot(tasks)

	const epsilon = 0.001
	if snap.Budget.WaveSpentUSD < 0.80-epsilon || snap.Budget.WaveSpentUSD > 0.80+epsilon {
		t.Errorf("WaveSpentUSD = %f, want ~0.80", snap.Budget.WaveSpentUSD)
	}
}

func TestBuildStateSnapshot_RecentEvents(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recent-events.db")
	auditLog, err := audit.NewSQLiteAuditLogger(dbPath)
	if err != nil {
		t.Fatalf("creating audit logger: %v", err)
	}

	ctx := context.Background()
	for i := range 15 {
		_ = auditLog.Record(ctx, audit.AuditEvent{
			Timestamp: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
			EventType: "task.dispatched",
			TaskID:    strPtr("t1"),
		})
	}

	cfg := config.DefaultConfig()
	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "",
		Tracker:      tracker.NewMockTracker(nil),
		Runner:       newNoopRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
		AuditLogger:  auditLog,
	})

	snap := orch.buildStateSnapshot(nil)

	if len(snap.RecentEvents) != 10 {
		t.Fatalf("expected 10 recent events, got %d", len(snap.RecentEvents))
	}
	if snap.RecentEvents[0].Type != "task.dispatched" {
		t.Errorf("first event type = %q, want 'task.dispatched'", snap.RecentEvents[0].Type)
	}

	auditLog.Close()
}

func TestDAGEdges_DispatchBlockedByUnmetDep(t *testing.T) {
	tasks := []types.Task{
		{ID: "a", Title: "A", Status: types.StatusRunning, Labels: []string{"todo"}, Priority: 1, CreatedAt: time.Now()},
		{ID: "b", Title: "B", Status: types.StatusQueued, Labels: []string{"todo"}, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	dispatched := map[string]bool{}
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		dispatched[opts.Prompt] = true
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
	})

	// B depends on A
	orch.mu.Lock()
	orch.taskDeps["b"] = []string{"a"}
	orch.mu.Unlock()

	// Mechanical dispatch should skip B since A is not terminal
	orch.mechanicalDispatch(context.Background(), tasks)

	// Wait for dispatch to finish
	orch.wg.Wait()

	// Only A should have been dispatched (already running doesn't re-dispatch, but B should be skipped)
	// Actually A is "running" so it gets skipped too. Let's just verify B was not dispatched
	orch.mu.Lock()
	_, bActive := orch.active["b"]
	orch.mu.Unlock()

	if bActive {
		t.Error("task B should not be dispatched while dependency A is non-terminal")
	}
}

func TestDAGEdges_DispatchAllowedWhenDepMet(t *testing.T) {
	tasks := []types.Task{
		{ID: "a", Title: "A", Status: types.StatusCompleted, Labels: []string{"done"}, Priority: 1, CreatedAt: time.Now()},
		{ID: "b", Title: "B", Status: types.StatusQueued, Labels: []string{"todo"}, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	dispatched := false
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
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
	})

	// B depends on A, but A is completed (terminal)
	orch.mu.Lock()
	orch.taskDeps["b"] = []string{"a"}
	orch.mu.Unlock()

	orch.mechanicalDispatch(context.Background(), tasks)
	orch.wg.Wait()

	if !dispatched {
		t.Error("task B should be dispatched when dependency A is terminal")
	}
}

func TestDAGEdges_CreateSubtasksStoresDeps(t *testing.T) {
	trk := tracker.NewMockTracker([]types.Task{
		{ID: "1", Title: "Parent", Status: types.StatusQueued},
	})

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       newNoopRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	tasks := []types.Task{{ID: "1", Title: "Parent", Status: types.StatusQueued}}
	actions := []Action{
		{
			Type: ActionCreateSubtasks,
			Subtasks: []SubtaskDef{
				{Title: "Step 1", Body: "First"},
				{Title: "Step 2", Body: "Second", DependsOn: []string{"step-1-id"}},
			},
		},
	}

	orch.executeActions(context.Background(), tasks, actions)

	// Check that at least one subtask has deps stored
	orch.mu.Lock()
	hasDeps := false
	for _, deps := range orch.taskDeps {
		if len(deps) > 0 {
			hasDeps = true
		}
	}
	orch.mu.Unlock()

	if !hasDeps {
		t.Error("expected at least one subtask to have stored dependencies")
	}
}

func TestDAGEdges_WaveExhaustionIgnoresBlockedByDeps(t *testing.T) {
	tasks := []types.Task{
		{ID: "a", Title: "A", Status: types.StatusCompleted},
		{ID: "b", Title: "B", Status: types.StatusQueued},
	}
	trk := tracker.NewMockTracker(tasks)

	cfg := config.DefaultConfig()
	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "",
		Tracker:      trk,
		Runner:       newNoopRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	// B depends on A (which is still non-terminal from wave perspective, but let's say
	// it has an in-progress dep). Actually: A is completed, B is queued with unmet dep
	// on a non-existent task "c" — so depsAreMet returns false.
	orch.mu.Lock()
	orch.taskDeps["b"] = []string{"c"}
	orch.mu.Unlock()

	orch.currentWave = &Wave{
		ID:              "wave-1",
		FrontierTaskIDs: []string{"a", "b"},
		Status:          "active",
	}

	// A is terminal, B has unmet deps → wave should be exhausted
	if !orch.isWaveExhausted() {
		t.Error("wave should be exhausted: A is terminal, B has unmet deps (should not block)")
	}
}

func TestDAGEdges_StatePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	deps := map[string][]string{
		"task-2": {"task-1"},
		"task-3": {"task-1", "task-2"},
	}

	if err := SaveState(path, nil, cost.NewTracker(), deps); err != nil {
		t.Fatalf("SaveState error: %v", err)
	}

	state, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState error: %v", err)
	}

	if len(state.TaskDeps) != 2 {
		t.Fatalf("expected 2 task deps, got %d", len(state.TaskDeps))
	}
	if len(state.TaskDeps["task-3"]) != 2 {
		t.Errorf("task-3 deps = %v, want 2 entries", state.TaskDeps["task-3"])
	}
}

func TestPromoteKnowledge_WritesFile(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Workspace.Root = tmpDir

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "",
		Tracker:      tracker.NewMockTracker(nil),
		Runner:       newNoopRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	action := Action{
		Type:    ActionPromoteKnowledge,
		Summary: "# Discovery\n\nThe auth middleware needs to validate tokens before rate limiting.",
	}

	err := orch.executePromoteKnowledge(action)
	if err != nil {
		t.Fatalf("executePromoteKnowledge error: %v", err)
	}

	// Check that a file was created in docs/exec-plans/
	entries, err := os.ReadDir(filepath.Join(tmpDir, "docs", "exec-plans"))
	if err != nil {
		t.Fatalf("reading exec-plans dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in exec-plans, got %d", len(entries))
	}
	if !strings.HasSuffix(entries[0].Name(), ".md") {
		t.Errorf("expected .md file, got %q", entries[0].Name())
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "docs", "exec-plans", entries[0].Name()))
	if err != nil {
		t.Fatalf("reading knowledge file: %v", err)
	}
	if !strings.Contains(string(data), "auth middleware") {
		t.Error("knowledge file should contain the summary content")
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Hello World", "hello-world"},
		{"Fix: auth-middleware bug", "fix-auth-middleware-bug"},
		{"Test 123!", "test-123"},
	}
	for _, tt := range tests {
		got := sanitizeFilename(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLoadKnowledge_ReadsRecentFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "exec-plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	for i := range 7 {
		name := fmt.Sprintf("2026-01-%02d-discovery.md", i+1)
		content := fmt.Sprintf("# Discovery %d\nSome content.", i+1)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	knowledge := loadKnowledge(dir)
	if knowledge == "" {
		t.Fatal("expected non-empty knowledge")
	}

	// Should only include last 5 files (3-7)
	if strings.Contains(knowledge, "Discovery 1") {
		t.Error("should not include oldest files (only last 5)")
	}
	if !strings.Contains(knowledge, "Discovery 7") {
		t.Error("should include most recent file")
	}
}

func TestReviewLoop_PassedCompletes(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "T1", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	callCount := 0
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		callCount++
		if callCount == 1 {
			// Executor
			return &types.RunResult{SessionID: "s1", ExitCode: 0, Output: "done", CostUSD: 0.10, Duration: time.Millisecond}, nil
		}
		// Reviewer — passes
		return &types.RunResult{SessionID: "s2", ExitCode: 0, Output: `{"passed":true,"feedback":""}`, CostUSD: 0.02}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 100000
	cfg.Agent.ReviewEnabled = true
	cfg.Agent.ReviewMaxRetries = 1

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = orch.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	task, _ := trk.GetTask(context.Background(), "1")
	if task.Status != types.StatusCompleted {
		t.Errorf("task status = %v, want completed", task.Status)
	}
}

func TestReviewLoop_FailedTriggersRetry(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "T1", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)
	events := NewMockEventBus()

	callCount := 0
	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		callCount++
		if callCount%2 == 1 {
			// Executor
			return &types.RunResult{SessionID: "s1", ExitCode: 0, Output: "done", CostUSD: 0.10, Duration: time.Millisecond}, nil
		}
		// Reviewer — fails
		return &types.RunResult{SessionID: "s2", ExitCode: 0, Output: `{"passed":false,"feedback":"missing tests"}`, CostUSD: 0.02}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 100000
	cfg.Agent.ReviewEnabled = true
	cfg.Agent.ReviewMaxRetries = 1

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     events,
		Logger:       testLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = orch.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	hasReviewFailed := false
	for _, ev := range events.Published {
		if ev.Type == "task.review_failed" {
			hasReviewFailed = true
		}
	}
	if !hasReviewFailed {
		t.Error("expected task.review_failed event")
	}
}

func TestReviewLoop_ExceedsMaxRetriesSkips(t *testing.T) {
	tasks := []types.Task{
		{ID: "1", Identifier: "GH-1", Title: "T1", Labels: []string{"todo"}, Status: types.StatusQueued, Priority: 1, CreatedAt: time.Now()},
	}
	trk := tracker.NewMockTracker(tasks)

	runner := agent.NewMockRunner()
	runner.RunFunc = func(_ context.Context, opts types.RunOpts) (*types.RunResult, error) {
		// Reviewer always fails
		return &types.RunResult{SessionID: "s1", ExitCode: 0, Output: `{"passed":false,"feedback":"bad"}`, CostUSD: 0.01}, nil
	}

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 100000
	cfg.Agent.ReviewEnabled = true
	cfg.Agent.ReviewMaxRetries = 0

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       runner,
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	// Pre-seed review retries to exceed max
	orch.mu.Lock()
	orch.reviewRetries["1"] = 1
	orch.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = orch.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	task, _ := trk.GetTask(context.Background(), "1")
	if task.Status != types.StatusCompleted {
		t.Errorf("task status = %v, want completed (review-skipped)", task.Status)
	}

	hasReviewSkipped := false
	for _, l := range task.Labels {
		if l == "review-skipped" {
			hasReviewSkipped = true
		}
	}
	if !hasReviewSkipped {
		t.Error("expected review-skipped label")
	}
}

func TestBuildStateSnapshot_IncludesDependsOn(t *testing.T) {
	cfg := config.DefaultConfig()
	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "",
		Tracker:      tracker.NewMockTracker(nil),
		Runner:       newNoopRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	orch.mu.Lock()
	orch.taskDeps["t2"] = []string{"t1"}
	orch.mu.Unlock()

	tasks := []types.Task{
		{ID: "t1", Status: types.StatusQueued},
		{ID: "t2", Status: types.StatusQueued},
	}

	snap := orch.buildStateSnapshot(tasks)

	var t2Summary *TaskSummary
	for i := range snap.Tasks {
		if snap.Tasks[i].ID == "t2" {
			t2Summary = &snap.Tasks[i]
			break
		}
	}
	if t2Summary == nil {
		t.Fatal("expected t2 in snapshot")
	}
	if len(t2Summary.DependsOn) != 1 || t2Summary.DependsOn[0] != "t1" {
		t.Errorf("t2.DependsOn = %v, want [t1]", t2Summary.DependsOn)
	}
}

func TestCreateSubtasks_AutoAddsTodoLabel(t *testing.T) {
	trk := tracker.NewMockTracker([]types.Task{
		{ID: "1", Title: "Parent", Status: types.StatusQueued},
	})

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Tracker.Labels.Active = []string{"todo"}

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       newNoopRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	tasks := []types.Task{{ID: "1", Title: "Parent", Status: types.StatusQueued}}
	actions := []Action{
		{
			Type: ActionCreateSubtasks,
			Subtasks: []SubtaskDef{
				{Title: "No Status Label", Body: "body", Labels: []string{"type:feature", "priority:high"}},
			},
		},
	}

	orch.executeActions(context.Background(), tasks, actions)

	if len(trk.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(trk.Tasks))
	}
	created := trk.Tasks[1]
	if !slices.Contains(created.Labels, "todo") {
		t.Errorf("created task labels = %v, want 'todo' to be present", created.Labels)
	}
	if !slices.Contains(created.Labels, "type:feature") {
		t.Errorf("created task labels = %v, want 'type:feature' to be preserved", created.Labels)
	}
}

func TestCreateSubtasks_RemapsDependsOnOrdinals(t *testing.T) {
	trk := tracker.NewMockTracker([]types.Task{
		{ID: "1", Title: "Parent", Status: types.StatusQueued},
	})

	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       newNoopRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	tasks := []types.Task{{ID: "1", Title: "Parent", Status: types.StatusQueued}}
	actions := []Action{
		{
			Type: ActionCreateSubtasks,
			Subtasks: []SubtaskDef{
				{Title: "Step A", Body: "first"},
				{Title: "Step B", Body: "second"},
				{Title: "Step C", Body: "third", DependsOn: []string{"1", "2"}},
			},
		},
	}

	orch.executeActions(context.Background(), tasks, actions)

	// Mock tracker assigns IDs sequentially: len(Tasks)+1 for each.
	// Parent is ID "1", so subtasks get "2", "3", "4".
	// Step C (ID "4") depends on ordinals "1" and "2", which should remap to "2" and "3".
	orch.mu.Lock()
	deps := orch.taskDeps["4"]
	orch.mu.Unlock()

	if len(deps) != 2 {
		t.Fatalf("expected 2 deps for task 4, got %d: %v", len(deps), deps)
	}
	if deps[0] != "2" || deps[1] != "3" {
		t.Errorf("task 4 deps = %v, want [2 3] (remapped from ordinals [1 2])", deps)
	}
}

func TestCreateSubtasks_NoActiveLabelsConfigured(t *testing.T) {
	trk := tracker.NewMockTracker([]types.Task{
		{ID: "1", Title: "Parent", Status: types.StatusQueued},
	})

	cfg := config.DefaultConfig()
	// Labels.Active is nil by default — no auto-add should occur

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       newNoopRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})

	tasks := []types.Task{{ID: "1", Title: "Parent", Status: types.StatusQueued}}
	actions := []Action{
		{
			Type: ActionCreateSubtasks,
			Subtasks: []SubtaskDef{
				{Title: "Task", Body: "body", Labels: []string{"type:bug"}},
			},
		},
	}

	orch.executeActions(context.Background(), tasks, actions)

	if len(trk.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(trk.Tasks))
	}
	// Only the original label should be present, no "todo" added
	created := trk.Tasks[1]
	if len(created.Labels) != 1 || created.Labels[0] != "type:bug" {
		t.Errorf("created task labels = %v, want [type:bug] (no auto-add when Active is empty)", created.Labels)
	}
}
