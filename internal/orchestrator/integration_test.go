package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
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

	orchAgent := NewOrchestratorAgent(orchRunner, "", 100000, testLogger())

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

func TestExecuteActions_CreateSubtasks(t *testing.T) {
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
				{Title: "Subtask A", Body: "Do A", Labels: []string{"todo"}},
				{Title: "Subtask B", Body: "Do B", Labels: []string{"todo", "bug"}},
			},
		},
	}

	orch.executeActions(context.Background(), tasks, actions)

	// Original task + 2 subtasks = 3
	if len(trk.Tasks) != 3 {
		t.Fatalf("expected 3 tasks (1 original + 2 subtasks), got %d", len(trk.Tasks))
	}
	if trk.Tasks[1].Title != "Subtask A" {
		t.Errorf("subtask 1 title = %q, want 'Subtask A'", trk.Tasks[1].Title)
	}
	if trk.Tasks[2].Title != "Subtask B" {
		t.Errorf("subtask 2 title = %q, want 'Subtask B'", trk.Tasks[2].Title)
	}
	if len(trk.Tasks[2].Labels) != 2 {
		t.Errorf("subtask 2 labels = %v, want [todo bug]", trk.Tasks[2].Labels)
	}
}

func newTestAuditLogger(t *testing.T) *audit.SQLiteAuditLogger {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test-audit.db")
	logger, err := audit.NewSQLiteAuditLogger(dbPath)
	if err != nil {
		t.Fatalf("creating audit logger: %v", err)
	}
	t.Cleanup(func() { logger.Close() })
	return logger
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
