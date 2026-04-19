package runs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/execute"
)

// fixturePlan returns a minimal ExecutionPlan suitable for seeding a
// RunStartedEvent in tests.
func fixturePlan() *execute.ExecutionPlan {
	return &execute.ExecutionPlan{
		Steps: []execute.PlanStep{
			{ID: "s1", AgentID: "miyazaki", Description: "Generate sprites", Status: execute.StepPending},
			{ID: "s2", AgentID: "kaplan", Description: "Balance numbers", DependsOn: "s1", Status: execute.StepPending},
		},
		Gates: []execute.ApprovalGate{
			{ID: "g1", AfterStep: "s1", Prompt: "Approve sprites"},
		},
		Metadata: execute.PlanMetadata{
			Title:          "Test Plan",
			Description:    "desc",
			PlanGeneration: 3,
		},
	}
}

func TestAppendAndReplay_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	runPath := filepath.Join(dir, "plan.md.runs", "run_20260419T120000.000Z_gen3.jsonl")

	plan := fixturePlan()
	events := []Event{
		RunStartedEvent("plan-1", 3, plan, "owner-repo"),
		StepStartedEvent("s1", "miyazaki"),
		StepCompletedEvent("s1", "miyazaki", []execute.StepArtifact{{StepID: "s1", Path: "a.png", Kind: "image"}}),
		GateOpenedEvent("g1", "s1", "miyazaki", "Hayao", "Approve sprites", nil, []string{"approve", "revise", "abort"}, nil, "/chain?plan=plan-1&gate=g1"),
	}
	for _, e := range events {
		if err := Append(runPath, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	got, err := Replay(runPath)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(got) != len(events) {
		t.Fatalf("replay count = %d, want %d", len(got), len(events))
	}
	if got[0].Type != EventRunStarted || got[0].PlanID != "plan-1" || got[0].CompileGeneration != 3 {
		t.Errorf("run_started not round-tripped: %+v", got[0])
	}
	if got[0].Title != "Test Plan" {
		t.Errorf("title not preserved: got %q", got[0].Title)
	}
	if len(got[0].PlanSteps) != 2 {
		t.Errorf("plan steps not preserved: got %d", len(got[0].PlanSteps))
	}
	if got[3].Type != EventGateOpened || got[3].GateID != "g1" {
		t.Errorf("gate_opened not round-tripped: %+v", got[3])
	}
}

func TestAppend_CreatesDirectoryAndAppendsAtomically(t *testing.T) {
	dir := t.TempDir()
	runPath := filepath.Join(dir, "nested", "runs", "run_20260419T120000.000Z_gen1.jsonl")
	if err := Append(runPath, RunStartedEvent("p", 1, fixturePlan(), "slug")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := os.Stat(runPath); err != nil {
		t.Fatalf("expected run file to exist: %v", err)
	}

	if err := Append(runPath, RunCompletedEvent()); err != nil {
		t.Fatalf("append terminal: %v", err)
	}
	evs, err := Replay(runPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || !evs[1].IsTerminal() {
		t.Errorf("expected 2 events with terminal tail, got %+v", evs)
	}
}

func TestReplay_MissingFileReturnsError(t *testing.T) {
	_, err := Replay(filepath.Join(t.TempDir(), "nope.jsonl"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}

func TestReplay_TolerantToTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	runPath := filepath.Join(dir, "runs", "run_20260419T120000.000Z_gen1.jsonl")
	if err := Append(runPath, RunStartedEvent("p", 1, fixturePlan(), "slug")); err != nil {
		t.Fatal(err)
	}
	if err := Append(runPath, StepStartedEvent("s1", "miyazaki")); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-write: append a raw, unterminated, partial
	// JSON blob. Replay should return the two valid events and stop
	// cleanly at the garbage tail rather than erroring.
	f, err := os.OpenFile(runPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(`{"type":"step_completed","step_id":"s1"`)); err != nil {
		t.Fatal(err)
	}
	f.Close()

	evs, err := Replay(runPath)
	if err != nil {
		t.Fatalf("replay should tolerate truncated tail, got %v", err)
	}
	if len(evs) != 2 {
		t.Errorf("expected 2 valid events, got %d", len(evs))
	}
}

func TestNewRunLogPath_Shape(t *testing.T) {
	plan := "/root/plans/owner-repo/foo.plan.md"
	ts, _ := time.Parse(time.RFC3339Nano, "2026-04-19T12:00:00.123Z")
	path := NewRunLogPath(plan, 7, ts)
	want := filepath.Join(plan+RunsDirSuffix, "run_20260419T120000.123Z_gen7.jsonl")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if !filenameRE.MatchString(filepath.Base(path)) {
		t.Errorf("generated filename %q does not match regex", filepath.Base(path))
	}
}

func TestListActive_FindsNonTerminalRuns(t *testing.T) {
	root := t.TempDir()
	plan := filepath.Join(root, "plan-a.plan.md")
	// Run 1: completed -> must be filtered out
	completedPath := NewRunLogPath(plan, 1, time.Now().Add(-2*time.Hour))
	mustAppend(t, completedPath, RunStartedEvent("plan-a", 1, fixturePlan(), "proj"))
	mustAppend(t, completedPath, RunCompletedEvent())

	// Run 2: parked at gate -> must be returned
	activePath := NewRunLogPath(plan, 2, time.Now().Add(-1*time.Hour))
	mustAppend(t, activePath, RunStartedEvent("plan-a", 2, fixturePlan(), "proj"))
	mustAppend(t, activePath, StepStartedEvent("s1", "miyazaki"))
	mustAppend(t, activePath, StepCompletedEvent("s1", "miyazaki", nil))
	mustAppend(t, activePath, GateOpenedEvent("g1", "s1", "miyazaki", "", "", nil, []string{"approve"}, nil, ""))

	// Run 3: mid-step, no terminal -> must be returned for abort-on-restart.
	mids := NewRunLogPath(filepath.Join(root, "plan-b.plan.md"), 1, time.Now())
	mustAppend(t, mids, RunStartedEvent("plan-b", 1, fixturePlan(), "proj"))
	mustAppend(t, mids, StepStartedEvent("s1", "miyazaki"))

	active, err := ListActive(root)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("expected 2 active runs, got %d (%+v)", len(active), active)
	}
	// Order is by StartedAt ascending.
	if active[0].RunPath != activePath {
		t.Errorf("first active run = %s, want %s", active[0].RunPath, activePath)
	}
	if active[0].TailType != EventGateOpened {
		t.Errorf("first tail = %q, want gate_opened", active[0].TailType)
	}
	if active[1].TailType != EventStepStarted {
		t.Errorf("second tail = %q, want step_started", active[1].TailType)
	}
	if active[0].PlanID != "plan-a" {
		t.Errorf("plan_id = %q, want plan-a", active[0].PlanID)
	}
	if active[0].CompileGeneration != 2 {
		t.Errorf("compile gen = %d, want 2", active[0].CompileGeneration)
	}
}

func TestListActive_MissingDirectoryIsOK(t *testing.T) {
	active, err := ListActive(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Errorf("missing project dir should not error, got %v", err)
	}
	if len(active) != 0 {
		t.Errorf("expected empty slice, got %+v", active)
	}
}

func TestListActive_SkipsUnrecognizedFilenames(t *testing.T) {
	root := t.TempDir()
	runsDir := filepath.Join(root, "plan.plan.md.runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runsDir, "scratch.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runsDir, "run_bad.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	active, err := ListActive(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Errorf("expected zero active runs for stray files, got %+v", active)
	}
}

func mustAppend(t *testing.T, path string, e Event) {
	t.Helper()
	if err := Append(path, e); err != nil {
		t.Fatalf("append %s: %v", e.Type, err)
	}
}

// TestListAll_NewestFirstAcrossPlans seeds two plans with mixed
// terminal and live runs and verifies ListAll (a) includes both
// terminal and non-terminal runs, (b) orders the combined result
// strictly newest-first by StartedAt, and (c) exposes the per-run
// Snapshot / file paths the hydration wire needs.
func TestListAll_NewestFirstAcrossPlans(t *testing.T) {
	root := t.TempDir()
	planA := filepath.Join(root, "plan-a.plan.md")
	planB := filepath.Join(root, "plan-b.plan.md")
	base := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

	// Plan A, run 1: completed 2h ago.
	oldCompleted := NewRunLogPath(planA, 1, base.Add(-2*time.Hour))
	mustAppend(t, oldCompleted, RunStartedEvent("plan-a", 1, fixturePlan(), "proj"))
	mustAppend(t, oldCompleted, RunCompletedEvent())

	// Plan B, run 1: aborted 90m ago.
	midAborted := NewRunLogPath(planB, 1, base.Add(-90*time.Minute))
	mustAppend(t, midAborted, RunStartedEvent("plan-b", 1, fixturePlan(), "proj"))
	mustAppend(t, midAborted, RunAbortedEvent("user"))

	// Plan A, run 2: live 30m ago (parked at gate).
	liveGate := NewRunLogPath(planA, 2, base.Add(-30*time.Minute))
	mustAppend(t, liveGate, RunStartedEvent("plan-a", 2, fixturePlan(), "proj"))
	mustAppend(t, liveGate, StepStartedEvent("s1", "miyazaki"))
	mustAppend(t, liveGate, GateOpenedEvent("g1", "s1", "miyazaki", "", "", nil, []string{"approve"}, nil, ""))

	got, err := ListAll(root, 0)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (active + terminal across both plans) entries: %+v", len(got), got)
	}
	if got[0].RunPath != liveGate {
		t.Errorf("newest entry = %s, want liveGate %s", got[0].RunPath, liveGate)
	}
	if got[1].RunPath != midAborted {
		t.Errorf("middle entry = %s, want midAborted %s", got[1].RunPath, midAborted)
	}
	if got[2].RunPath != oldCompleted {
		t.Errorf("oldest entry = %s, want oldCompleted %s", got[2].RunPath, oldCompleted)
	}
	if got[0].Snapshot.Terminal {
		t.Errorf("live gate entry should be non-terminal, got terminal=true")
	}
	if !got[1].Snapshot.Terminal || got[1].Snapshot.Reason == "" {
		t.Errorf("aborted entry should be terminal with reason, got %+v", got[1].Snapshot)
	}
	if !got[2].Snapshot.Terminal {
		t.Errorf("completed entry should be terminal, got terminal=false")
	}
}

// TestListAll_LimitIsPerPlan ensures the `limit` parameter caps runs
// per .runs/ directory rather than globally so a project with many
// plans can still surface at least `limit` history entries for each
// plan. Files past the cap remain on disk (no deletion).
func TestListAll_LimitIsPerPlan(t *testing.T) {
	root := t.TempDir()
	planA := filepath.Join(root, "plan-a.plan.md")
	planB := filepath.Join(root, "plan-b.plan.md")
	base := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

	// Seed 5 runs per plan, all completed, spaced one minute apart.
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		pa := NewRunLogPath(planA, i+1, ts)
		mustAppend(t, pa, RunStartedEvent("plan-a", i+1, fixturePlan(), "proj"))
		mustAppend(t, pa, RunCompletedEvent())

		pb := NewRunLogPath(planB, i+1, ts.Add(30*time.Second))
		mustAppend(t, pb, RunStartedEvent("plan-b", i+1, fixturePlan(), "proj"))
		mustAppend(t, pb, RunCompletedEvent())
	}

	got, err := ListAll(root, 3)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	// 3 per plan * 2 plans = 6 total.
	if len(got) != 6 {
		t.Fatalf("len = %d, want 6 (limit=3 per plan × 2 plans)", len(got))
	}

	// Verify on-disk files outside the cap are untouched.
	entries, err := os.ReadDir(planA + RunsDirSuffix)
	if err != nil {
		t.Fatalf("read plan-a runs dir: %v", err)
	}
	var jsonlCount int
	for _, e := range entries {
		if !e.IsDir() && filenameRE.MatchString(e.Name()) {
			jsonlCount++
		}
	}
	if jsonlCount != 5 {
		t.Errorf("expected 5 run files on disk for plan-a (retention cap is read-only), got %d", jsonlCount)
	}
}

// TestListAll_DefaultLimitApplied verifies that a zero (or negative)
// limit falls back to DefaultHistoryLimit rather than returning
// unbounded results.
func TestListAll_DefaultLimitApplied(t *testing.T) {
	root := t.TempDir()
	plan := filepath.Join(root, "plan.plan.md")
	base := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

	for i := 0; i < DefaultHistoryLimit+5; i++ {
		p := NewRunLogPath(plan, i+1, base.Add(time.Duration(i)*time.Second))
		mustAppend(t, p, RunStartedEvent("plan", i+1, fixturePlan(), "proj"))
		mustAppend(t, p, RunCompletedEvent())
	}
	got, err := ListAll(root, 0)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(got) != DefaultHistoryLimit {
		t.Errorf("len = %d, want DefaultHistoryLimit=%d", len(got), DefaultHistoryLimit)
	}
}

// TestListAll_MissingDirectoryIsOK mirrors ListActive's contract: a
// non-existent project root returns an empty slice, not an error, so
// orchestrator startup on a fresh machine doesn't panic.
func TestListAll_MissingDirectoryIsOK(t *testing.T) {
	got, err := ListAll(filepath.Join(t.TempDir(), "missing"), 0)
	if err != nil {
		t.Errorf("missing project dir should not error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %+v", got)
	}
}
