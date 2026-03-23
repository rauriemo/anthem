package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/cost"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	"github.com/rauriemo/anthem/internal/workspace"
)

func defaultTestConfig() config.Config {
	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	cfg.Polling.IntervalMS = 100000
	return cfg
}

func newNoopTracker() *tracker.MockTracker {
	return tracker.NewMockTracker(nil)
}

func newNoopRunner() *agent.MockRunner {
	r := agent.NewMockRunner()
	r.RunFunc = func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
		return &types.RunResult{ExitCode: 0}, nil
	}
	return r
}

func newNoopWorkspace() *workspace.MockWorkspaceManager {
	return workspace.NewMockWorkspaceManager()
}

func TestSaveAndLoadStateRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	retryState := map[string]*RetryInfo{
		"task-1": {
			TaskID:      "task-1",
			Attempts:    3,
			NextRetryAt: time.Now().Add(40 * time.Second).Truncate(time.Millisecond),
			LastError:   "agent crashed",
		},
		"task-2": {
			TaskID:      "task-2",
			Attempts:    1,
			NextRetryAt: time.Now().Add(10 * time.Second).Truncate(time.Millisecond),
			LastError:   "timeout",
		},
	}

	ct := cost.NewTracker()
	ct.Record(cost.SessionCost{TaskID: "task-1", SessionID: "s1", TokensIn: 100, TokensOut: 50, CostUSD: 0.5, TurnsUsed: 3})
	ct.Record(cost.SessionCost{TaskID: "task-2", SessionID: "s2", TokensIn: 200, TokensOut: 100, CostUSD: 1.5, TurnsUsed: 5})

	if err := SaveState(path, retryState, ct); err != nil {
		t.Fatalf("SaveState() error: %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("state file was not created")
	}

	state, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}
	if state == nil {
		t.Fatal("LoadState() returned nil")
	}
	if state.Version != stateVersion {
		t.Errorf("Version = %d, want %d", state.Version, stateVersion)
	}

	if len(state.RetryState) != 2 {
		t.Fatalf("RetryState has %d entries, want 2", len(state.RetryState))
	}

	rs1 := state.RetryState["task-1"]
	if rs1 == nil {
		t.Fatal("missing retry state for task-1")
	}
	if rs1.Attempts != 3 {
		t.Errorf("task-1 Attempts = %d, want 3", rs1.Attempts)
	}
	if rs1.LastError != "agent crashed" {
		t.Errorf("task-1 LastError = %q, want %q", rs1.LastError, "agent crashed")
	}

	if len(state.CostSessions) != 2 {
		t.Fatalf("CostSessions has %d entries, want 2", len(state.CostSessions))
	}

	sessionMap := make(map[string]cost.SessionCost)
	for _, sc := range state.CostSessions {
		sessionMap[sc.SessionID] = sc
	}
	s1, ok := sessionMap["s1"]
	if !ok {
		t.Fatal("missing cost session s1")
	}
	if s1.CostUSD != 0.5 {
		t.Errorf("s1 CostUSD = %f, want 0.5", s1.CostUSD)
	}
	if s1.TokensIn != 100 {
		t.Errorf("s1 TokensIn = %d, want 100", s1.TokensIn)
	}
}

func TestLoadStateMissingFileReturnsZeroValue(t *testing.T) {
	state, err := LoadState(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}
	if state == nil {
		t.Fatal("expected zero-value state, got nil")
	}
	if state.Version != stateVersion {
		t.Errorf("Version = %d, want %d", state.Version, stateVersion)
	}
	if len(state.RetryState) != 0 {
		t.Errorf("RetryState should be empty, got %d", len(state.RetryState))
	}
	if len(state.CostSessions) != 0 {
		t.Errorf("CostSessions should be empty, got %d", len(state.CostSessions))
	}
}

func TestLoadStateErrorsOnInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{invalid json"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadState(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestLoadStateRejectsFutureVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	data := `{"version": 999, "saved_at": "2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadState(path)
	if err == nil {
		t.Error("expected error for future version")
	}
}

func TestSaveStateAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	for i := 0; i < 2; i++ {
		if err := SaveState(path, nil, cost.NewTracker()); err != nil {
			t.Fatalf("SaveState() error on iteration %d: %v", i, err)
		}
	}

	tmpPath := path + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("temp file should not exist after successful save")
	}
}

func TestSaveStateCreatesParentDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "state.json")

	if err := SaveState(path, nil, cost.NewTracker()); err != nil {
		t.Fatalf("SaveState() error: %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("state file should have been created in new subdirectory")
	}
}

func TestDefaultStatePath(t *testing.T) {
	p, err := DefaultStatePath()
	if err != nil {
		t.Fatalf("DefaultStatePath() error: %v", err)
	}
	if filepath.Base(p) != "state.json" {
		t.Errorf("DefaultStatePath() = %q, want filename state.json", p)
	}
	if filepath.Base(filepath.Dir(p)) != ".anthem" {
		t.Errorf("DefaultStatePath() parent should be .anthem, got %q", filepath.Dir(p))
	}
}

func TestLoadAndReconcileRestoresRetryState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Save state with retry entries for two tasks
	retryState := map[string]*RetryInfo{
		"task-active": {
			TaskID:      "task-active",
			Attempts:    2,
			NextRetryAt: time.Now().Add(20 * time.Second),
			LastError:   "previous error",
		},
		"task-done": {
			TaskID:      "task-done",
			Attempts:    1,
			NextRetryAt: time.Now().Add(10 * time.Second),
			LastError:   "some error",
		},
	}
	ct := cost.NewTracker()
	ct.Record(cost.SessionCost{TaskID: "task-active", SessionID: "s1", CostUSD: 1.0})
	ct.Record(cost.SessionCost{TaskID: "task-done", SessionID: "s2", CostUSD: 0.5})

	if err := SaveState(path, retryState, ct); err != nil {
		t.Fatal(err)
	}

	// Mock tracker: task-active is still active, task-done is completed (terminal)
	trk := tracker.NewMockTracker([]types.Task{
		{ID: "task-active", Status: types.StatusActive},
		{ID: "task-done", Status: types.StatusCompleted},
	})

	cfg := defaultTestConfig()
	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       newNoopRunner(),
		Workspace:    newNoopWorkspace(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
		StatePath:    path,
	})

	// Call LoadAndReconcile explicitly
	if err := orch.LoadAndReconcile(context.Background()); err != nil {
		t.Fatalf("LoadAndReconcile() error: %v", err)
	}

	// Retry state for active task should be restored
	orch.mu.Lock()
	riActive, hasActive := orch.retryState["task-active"]
	_, hasDone := orch.retryState["task-done"]
	orch.mu.Unlock()

	if !hasActive {
		t.Fatal("retry state for task-active was not restored")
	}
	if riActive.Attempts != 2 {
		t.Errorf("task-active Attempts = %d, want 2", riActive.Attempts)
	}

	// Retry state for terminal task should NOT be restored
	if hasDone {
		t.Error("retry state for task-done should have been skipped (terminal)")
	}

	// Cost sessions for both tasks should still be restored (always kept)
	if orch.costTracker.TaskCost("task-active") != 1.0 {
		t.Errorf("task-active cost = %f, want 1.0", orch.costTracker.TaskCost("task-active"))
	}
	if orch.costTracker.TaskCost("task-done") != 0.5 {
		t.Errorf("task-done cost = %f, want 0.5 (cost sessions preserved even for terminal)", orch.costTracker.TaskCost("task-done"))
	}
}

func TestLoadAndReconcileSkipsMissingTasks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	retryState := map[string]*RetryInfo{
		"task-gone": {
			TaskID:      "task-gone",
			Attempts:    1,
			NextRetryAt: time.Now().Add(10 * time.Second),
			LastError:   "error",
		},
	}

	if err := SaveState(path, retryState, cost.NewTracker()); err != nil {
		t.Fatal(err)
	}

	// Empty tracker — task doesn't exist
	trk := tracker.NewMockTracker(nil)

	cfg := defaultTestConfig()
	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       newNoopRunner(),
		Workspace:    newNoopWorkspace(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
		StatePath:    path,
	})

	if err := orch.LoadAndReconcile(context.Background()); err != nil {
		t.Fatalf("LoadAndReconcile() error: %v", err)
	}

	// Task-gone should be skipped (GetTask returns nil)
	orch.mu.Lock()
	_, hasGone := orch.retryState["task-gone"]
	orch.mu.Unlock()

	if hasGone {
		t.Error("retry state for missing task should not be restored")
	}
}

func TestShutdownSavesState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	cfg := defaultTestConfig()
	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      newNoopTracker(),
		Runner:       newNoopRunner(),
		Workspace:    newNoopWorkspace(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
		StatePath:    path,
	})

	orch.mu.Lock()
	orch.retryState["task-42"] = &RetryInfo{
		TaskID:      "task-42",
		Attempts:    1,
		NextRetryAt: time.Now().Add(10 * time.Second),
		LastError:   "test error",
	}
	orch.mu.Unlock()

	orch.costTracker.Record(cost.SessionCost{
		TaskID: "task-42", SessionID: "s99", CostUSD: 2.5,
	})

	orch.Shutdown()

	state, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}
	if len(state.RetryState) != 1 {
		t.Errorf("expected 1 retry entry, got %d", len(state.RetryState))
	}
	if len(state.CostSessions) != 1 {
		t.Errorf("expected 1 cost session, got %d", len(state.CostSessions))
	}
}

func TestRunCallsLoadAndReconcile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Save state with a retry entry for an active task
	retryState := map[string]*RetryInfo{
		"task-1": {
			TaskID:      "task-1",
			Attempts:    2,
			NextRetryAt: time.Now().Add(20 * time.Second),
			LastError:   "previous error",
		},
	}
	ct := cost.NewTracker()
	ct.Record(cost.SessionCost{TaskID: "task-1", SessionID: "s1", CostUSD: 1.0})

	if err := SaveState(path, retryState, ct); err != nil {
		t.Fatal(err)
	}

	trk := tracker.NewMockTracker([]types.Task{
		{ID: "task-1", Status: types.StatusActive, Labels: []string{"todo"}, CreatedAt: time.Now()},
	})

	cfg := defaultTestConfig()
	cfg.Polling.IntervalMS = 100000

	orch := New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      trk,
		Runner:       newNoopRunner(),
		Workspace:    newNoopWorkspace(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
		StatePath:    path,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_ = orch.Run(ctx)

	// Verify state was loaded during Run
	orch.mu.Lock()
	ri, ok := orch.retryState["task-1"]
	orch.mu.Unlock()
	if !ok {
		t.Fatal("retry state for task-1 was not restored by Run()")
	}
	if ri.Attempts != 2 {
		t.Errorf("task-1 Attempts = %d, want 2", ri.Attempts)
	}

	if orch.costTracker.TaskCost("task-1") != 1.0 {
		t.Errorf("task-1 cost = %f, want 1.0", orch.costTracker.TaskCost("task-1"))
	}
}
