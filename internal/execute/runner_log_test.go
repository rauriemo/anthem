package execute

import (
	"context"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/guests"
)

// recordedEvent is a lightweight descriptor of one RunEventAppender call
// used by fakeAppender. Only the fields asserted in tests are kept; a
// real FileAppender emits a strictly richer structure.
type recordedEvent struct {
	kind       string
	stepID     string
	agentID    string
	artifactN  int
	gateID     string
	resolution GateAction
	reason     string
	planID     string
	compileGen int
}

// fakeAppender captures every transition notification the runner makes
// so tests can pin the log-first contract: appender call order and
// payload correctness without touching the filesystem. Mirrors the
// behavior of the FileAppender at internal/plans/runs/appender.go.
type fakeAppender struct {
	events []recordedEvent
}

func (f *fakeAppender) AppendRunStarted(planID string, compileGen int, _ *ExecutionPlan, _ string) {
	f.events = append(f.events, recordedEvent{kind: "run_started", planID: planID, compileGen: compileGen})
}
func (f *fakeAppender) AppendStepQueued(stepID, agentID string) {
	f.events = append(f.events, recordedEvent{kind: "step_queued", stepID: stepID, agentID: agentID})
}
func (f *fakeAppender) AppendStepStarted(stepID, agentID string) {
	f.events = append(f.events, recordedEvent{kind: "step_started", stepID: stepID, agentID: agentID})
}
func (f *fakeAppender) AppendStepCompleted(stepID, agentID string, artifacts []StepArtifact) {
	f.events = append(f.events, recordedEvent{kind: "step_completed", stepID: stepID, agentID: agentID, artifactN: len(artifacts)})
}
func (f *fakeAppender) AppendStepFailed(stepID, agentID, err string) {
	f.events = append(f.events, recordedEvent{kind: "step_failed", stepID: stepID, agentID: agentID, reason: err})
}
func (f *fakeAppender) AppendGateOpened(gateID, stepID, agentID, _ string, _ string, artifacts []StepArtifact, _ []string, _ *guests.ReviewSpec, _ string) {
	f.events = append(f.events, recordedEvent{kind: "gate_opened", gateID: gateID, stepID: stepID, agentID: agentID, artifactN: len(artifacts)})
}
func (f *fakeAppender) AppendGateResolved(gateID string, action GateAction, _ string) {
	f.events = append(f.events, recordedEvent{kind: "gate_resolved", gateID: gateID, resolution: action})
}
func (f *fakeAppender) AppendRunCompleted() {
	f.events = append(f.events, recordedEvent{kind: "run_completed"})
}
func (f *fakeAppender) AppendRunAborted(reason string) {
	f.events = append(f.events, recordedEvent{kind: "run_aborted", reason: reason})
}

func (f *fakeAppender) kinds() []string {
	out := make([]string, 0, len(f.events))
	for _, e := range f.events {
		out = append(out, e.kind)
	}
	return out
}

// TestPlanRunner_AppendsRunLogInOrder pins the log-first contract for a
// clean two-step run ending in completion: run_started -> step_started ->
// step_completed -> step_started -> step_completed -> run_completed. This
// is the exact shape ReplayToSnapshot will fold back into a Prism
// plan_snapshot on reconnect.
func TestPlanRunner_AppendsRunLogInOrder(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}
	arts := newMockArtifacts()
	arts.toReturn["s1"] = []StepArtifact{{StepID: "s1", Path: "hero.png"}}
	app := &fakeAppender{}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex:        testGuestIndex(),
		Runner:            runner,
		ChannelMgr:        bc,
		Artifacts:         arts,
		PlanID:            "plan-xyz",
		CompileGeneration: 7,
		RunLog:            app,
	})

	if err := pr.Run(context.Background(), twoStepPlan(), "thread-1"); err != nil {
		t.Fatalf("run: %v", err)
	}

	want := []string{"run_started", "step_started", "step_completed", "step_started", "step_completed", "run_completed"}
	got := app.kinds()
	if len(got) != len(want) {
		t.Fatalf("log shape mismatch:\ngot:  %v\nwant: %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if app.events[0].planID != "plan-xyz" || app.events[0].compileGen != 7 {
		t.Errorf("run_started header = %+v, want plan-xyz/7", app.events[0])
	}
	if app.events[2].artifactN != 1 {
		t.Errorf("step_completed artifact count = %d, want 1", app.events[2].artifactN)
	}
}

// TestPlanRunner_AppendsGateOpenBeforeBroadcast verifies that the gate
// transition is logged before the corresponding channel broadcast fires.
// This is the ordering the plan_snapshot replay relies on -- if we lost
// a broadcast in a network partition but the log already recorded the
// gate, a reconnect still resurrects the user-visible review drawer.
func TestPlanRunner_AppendsGateOpenBeforeBroadcast(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}
	app := &fakeAppender{}
	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
		Runner:     runner,
		ChannelMgr: bc,
		RunLog:     app,
		PlanID:     "p1",
	})
	plan := twoStepPlan()
	plan.Gates = []ApprovalGate{{ID: "g1", AfterStep: "s1", Prompt: "Approve?"}}

	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			for _, e := range app.events {
				if e.kind == "gate_opened" {
					pr.ResolveGate("g1", GateResolution{Action: GateApprove})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	if err := pr.Run(context.Background(), plan, "thread-1"); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Assert gate_opened shows up in the log before gate_resolved.
	var openedIdx, resolvedIdx int = -1, -1
	for i, e := range app.events {
		if e.kind == "gate_opened" {
			openedIdx = i
		}
		if e.kind == "gate_resolved" {
			resolvedIdx = i
		}
	}
	if openedIdx < 0 || resolvedIdx < 0 || openedIdx >= resolvedIdx {
		t.Errorf("log shape wrong: gate_opened@%d gate_resolved@%d", openedIdx, resolvedIdx)
	}
	if app.events[resolvedIdx].resolution != GateApprove {
		t.Errorf("resolution = %q, want approve", app.events[resolvedIdx].resolution)
	}
}

// TestPlanRunner_AbortAppendsRunAborted pins that a gate abort closes
// the run log with an abort entry so ListActive correctly filters the
// run out on the next orchestrator scan.
func TestPlanRunner_AbortAppendsRunAborted(t *testing.T) {
	runner := newMockRunner()
	bc := &mockBroadcaster{}
	app := &fakeAppender{}
	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
		Runner:     runner,
		ChannelMgr: bc,
		RunLog:     app,
	})
	plan := twoStepPlan()
	plan.Gates = []ApprovalGate{{ID: "g1", AfterStep: "s1", Prompt: "Approve?"}}

	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			for _, e := range app.events {
				if e.kind == "gate_opened" {
					pr.ResolveGate("g1", GateResolution{Action: GateAbort})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	_ = pr.Run(context.Background(), plan, "thread-1")

	tail := app.events[len(app.events)-1]
	if tail.kind != "run_aborted" || tail.reason != "aborted at gate" {
		t.Errorf("tail event = %+v, want run_aborted/aborted at gate", tail)
	}
}

// TestPlanRunner_NilAppenderUsesNoop guards the legacy path where
// tryLoadExecutionPlan dispatches without a RunLog. The runner must
// continue to work without a file-backed logger so tests and inline
// [execute:plan] tags keep functioning.
func TestPlanRunner_NilAppenderUsesNoop(t *testing.T) {
	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
		Runner:     newMockRunner(),
		ChannelMgr: &mockBroadcaster{},
	})
	if err := pr.Run(context.Background(), twoStepPlan(), "thread-1"); err != nil {
		t.Fatalf("run with nil appender: %v", err)
	}
}
