package execute

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/types"
)

// mockRunnerWithOutput returns a fixed (exit code, output) pair so the
// runner's post-Run parsing path can be exercised without the standard
// mockRunner's prompt-substring dispatch (which routes happy paths
// through the generic "ok" output and ignores the per-call mutation
// we need for blocked_on assertions).
type mockRunnerWithOutput struct {
	mu       sync.Mutex
	calls    int
	output   string
	exitCode int
}

func (m *mockRunnerWithOutput) Run(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return &types.RunResult{ExitCode: m.exitCode, Output: m.output}, nil
}

func (m *mockRunnerWithOutput) Continue(_ context.Context, _ string, _ string, _ types.ContinueOpts) (*types.RunResult, error) {
	return nil, nil
}

func (m *mockRunnerWithOutput) Kill(_ int) error { return nil }

func singleStepPlan(t *testing.T) *ExecutionPlan {
	t.Helper()
	return &ExecutionPlan{
		Steps: []PlanStep{
			{ID: "s1", AgentID: "artist", Description: "Draw sprites", Status: StepPending},
		},
		Metadata: PlanMetadata{Title: "Blocked-on Test", CreatedAt: time.Now()},
	}
}

// stepFailedKinds is the canonical ordered kinds emitted on the
// blocked_on path: run_started -> step_started -> step_failed ->
// (user resolves with abort) -> run_aborted. step_completed must
// NOT appear — that's the whole point of the fix.
func assertNoStepCompleted(t *testing.T, app *fakeAppender) {
	t.Helper()
	for _, e := range app.kinds() {
		if e == "step_completed" {
			t.Fatalf("blocked_on step should not emit step_completed; got kinds=%v", app.kinds())
		}
	}
}

// TestRunStep_FailsOnBlockedOn pins the core fix: an agent that exits
// 0 but reports a non-empty blocked_on must be routed through
// handleStepFailure instead of the silent step_completed path. See
// embercoil-serpent incident 2026-04-21 for the motivating bug —
// Walt exited 0 with blocked_on: "<slug> path not found" and the
// runner marked the step completed in 40 seconds without doing any
// work.
func TestRunStep_FailsOnBlockedOn(t *testing.T) {
	mr := &mockRunnerWithOutput{
		exitCode: 0,
		output: `Draft done.
{"context_report":{"action":"task_completed","summary":"attempted sprite","blocked_on":"missing <slug> path"}}`,
	}
	bc := &mockBroadcaster{}
	app := &fakeAppender{}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
		Runner:     mr,
		ChannelMgr: bc,
		RunLog:     app,
	})

	plan := singleStepPlan(t)

	// Abort at the first step_failed so Run returns instead of
	// blocking forever on ResolveFailure.
	go func() {
		for {
			for _, et := range bc.eventTypes() {
				if et == EventStepFailed {
					pr.ResolveFailure("s1", GateResolution{Action: GateAbort})
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	if err := pr.Run(context.Background(), plan, "thread-blocked"); err == nil {
		t.Fatal("expected Run to return an abort error after blocked_on failure")
	}

	assertNoStepCompleted(t, app)

	var gotFailure *recordedEvent
	for i := range app.events {
		if app.events[i].kind == "step_failed" && app.events[i].stepID == "s1" {
			gotFailure = &app.events[i]
			break
		}
	}
	if gotFailure == nil {
		t.Fatalf("expected step_failed event; got kinds=%v", app.kinds())
	}
	if gotFailure.reason == "" {
		t.Fatal("step_failed reason should carry the agent's blocked_on message")
	}
	if !containsSubstring(gotFailure.reason, "blocked_on") {
		t.Errorf("step_failed reason = %q, expected to include 'blocked_on'", gotFailure.reason)
	}
	if !containsSubstring(gotFailure.reason, "missing <slug> path") {
		t.Errorf("step_failed reason = %q, expected to preserve agent's blocked_on detail", gotFailure.reason)
	}
}

// TestRunStep_IgnoresEmptyBlockedOn is the backward-compat pin: an
// agent that includes a context_report with blocked_on explicitly
// empty (or whitespace-only) must still be treated as success.
// Empty blocked_on is the grammar's "not blocked" signal and
// flipping it to a failure would break every happy-path agent
// that conscientiously reports an empty blocked_on.
func TestRunStep_IgnoresEmptyBlockedOn(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{
			name:   "empty_string",
			output: `{"context_report":{"action":"task_completed","summary":"done","blocked_on":""}}`,
		},
		{
			name:   "whitespace_only",
			output: `{"context_report":{"action":"task_completed","summary":"done","blocked_on":"   "}}`,
		},
		{
			name:   "field_omitted",
			output: `{"context_report":{"action":"task_completed","summary":"done"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mr := &mockRunnerWithOutput{exitCode: 0, output: tc.output}
			bc := &mockBroadcaster{}
			app := &fakeAppender{}

			pr := NewPlanRunner(RunnerOpts{
				GuestIndex: testGuestIndex(),
				Runner:     mr,
				ChannelMgr: bc,
				RunLog:     app,
			})

			if err := pr.Run(context.Background(), singleStepPlan(t), "thread-empty"); err != nil {
				t.Fatalf("Run returned error on empty blocked_on: %v", err)
			}

			var sawCompleted bool
			for _, e := range app.events {
				if e.kind == "step_completed" && e.stepID == "s1" {
					sawCompleted = true
				}
				if e.kind == "step_failed" {
					t.Fatalf("unexpected step_failed for empty blocked_on (%s): %+v", tc.name, e)
				}
			}
			if !sawCompleted {
				t.Fatalf("expected step_completed event; got kinds=%v", app.kinds())
			}
		})
	}
}

// TestRunStep_IgnoresMissingReport pins that output with no
// context_report block at all stays on the success path (the
// report is optional per the Context Reporting block in
// prompt.contextReportingBlock).
func TestRunStep_IgnoresMissingReport(t *testing.T) {
	mr := &mockRunnerWithOutput{
		exitCode: 0,
		output:   "All done, no structured report here.",
	}
	bc := &mockBroadcaster{}
	app := &fakeAppender{}

	pr := NewPlanRunner(RunnerOpts{
		GuestIndex: testGuestIndex(),
		Runner:     mr,
		ChannelMgr: bc,
		RunLog:     app,
	})

	if err := pr.Run(context.Background(), singleStepPlan(t), "thread-noreport"); err != nil {
		t.Fatalf("Run returned error on missing report: %v", err)
	}

	var sawCompleted bool
	for _, e := range app.events {
		if e.kind == "step_completed" && e.stepID == "s1" {
			sawCompleted = true
		}
		if e.kind == "step_failed" {
			t.Fatalf("unexpected step_failed when output has no context_report: %+v", e)
		}
	}
	if !sawCompleted {
		t.Fatalf("expected step_completed event; got kinds=%v", app.kinds())
	}
}
