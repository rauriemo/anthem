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
