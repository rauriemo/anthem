package runs

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/execute"
	"github.com/rauriemo/anthem/internal/guests"
)

func TestReplayToSnapshot_RunningAtGate(t *testing.T) {
	dir := t.TempDir()
	runPath := filepath.Join(dir, "plan.plan.md.runs", "run_20260419T120000.000Z_gen3.jsonl")

	plan := fixturePlan()
	review := &guests.ReviewSpec{Kind: "image-gallery", Title: "Sprite review"}
	arts := []execute.StepArtifact{{StepID: "s1", Path: "out/a.png", Kind: "image", ArtifactID: "art-abc"}}

	for _, e := range []Event{
		RunStartedEvent("plan-xyz", 3, plan, "owner-repo"),
		StepStartedEvent("s1", "miyazaki"),
		StepCompletedEvent("s1", "miyazaki", arts),
		GateOpenedEvent("g1", "s1", "miyazaki", "Hayao", "Approve sprites", arts, []string{"approve", "revise", "abort"}, review, "/chain?plan=plan-xyz&gate=g1"),
	} {
		if err := Append(runPath, e); err != nil {
			t.Fatal(err)
		}
	}

	snap, err := ReplayToSnapshot(runPath)
	if err != nil {
		t.Fatalf("replay to snapshot: %v", err)
	}
	if snap.PlanID != "plan-xyz" || snap.CompileGeneration != 3 || snap.Title != "Test Plan" {
		t.Errorf("header not propagated: %+v", snap)
	}
	if len(snap.Steps) != 2 {
		t.Fatalf("steps count = %d, want 2", len(snap.Steps))
	}
	if snap.Steps[0].Status != execute.StepCompleted {
		t.Errorf("s1 status = %q, want completed", snap.Steps[0].Status)
	}
	if snap.Steps[1].Status != execute.StepPending {
		t.Errorf("s2 status = %q, want pending", snap.Steps[1].Status)
	}
	if len(snap.Steps[0].Artifacts) != 1 || snap.Steps[0].Artifacts[0].Path != "out/a.png" {
		t.Errorf("artifacts not captured: %+v", snap.Steps[0].Artifacts)
	}
	if snap.OpenGate == nil {
		t.Fatal("expected open gate")
	}
	if snap.OpenGate.ID != "g1" || snap.OpenGate.Review == nil || snap.OpenGate.Review.Kind != "image-gallery" {
		t.Errorf("open gate incomplete: %+v", snap.OpenGate)
	}
	if snap.OpenFailure != nil {
		t.Errorf("unexpected open failure: %+v", snap.OpenFailure)
	}
}

func TestReplayToSnapshot_GateResolvedClosesGate(t *testing.T) {
	events := []Event{
		RunStartedEvent("p", 1, fixturePlan(), "slug"),
		StepStartedEvent("s1", "miyazaki"),
		StepCompletedEvent("s1", "miyazaki", nil),
		GateOpenedEvent("g1", "s1", "miyazaki", "Hayao", "approve?", nil, []string{"approve"}, nil, ""),
		GateResolvedEvent("g1", execute.GateApprove, ""),
		StepStartedEvent("s2", "kaplan"),
	}
	snap, err := EventsToSnapshot(events)
	if err != nil {
		t.Fatal(err)
	}
	if snap.OpenGate != nil {
		t.Errorf("gate should be closed after gate_resolved, got %+v", snap.OpenGate)
	}
	if snap.Steps[1].Status != execute.StepRunning {
		t.Errorf("s2 should be running after resume, got %q", snap.Steps[1].Status)
	}
}

func TestReplayToSnapshot_ReviseReopensStep(t *testing.T) {
	events := []Event{
		RunStartedEvent("p", 1, fixturePlan(), "slug"),
		StepStartedEvent("s1", "miyazaki"),
		StepCompletedEvent("s1", "miyazaki", nil),
		GateOpenedEvent("g1", "s1", "miyazaki", "", "", nil, []string{"revise"}, nil, ""),
		GateResolvedEvent("g1", execute.GateRevise, "try again"),
	}
	snap, err := EventsToSnapshot(events)
	if err != nil {
		t.Fatal(err)
	}
	if snap.OpenGate != nil {
		t.Errorf("gate should be closed after revise, got %+v", snap.OpenGate)
	}
	if snap.Steps[0].Status != execute.StepPending {
		t.Errorf("s1 should be pending after revise, got %q", snap.Steps[0].Status)
	}
}

func TestReplayToSnapshot_StepFailureSurfaced(t *testing.T) {
	events := []Event{
		RunStartedEvent("p", 1, fixturePlan(), "slug"),
		StepStartedEvent("s1", "miyazaki"),
		StepFailedEvent("s1", "miyazaki", "boom"),
	}
	snap, err := EventsToSnapshot(events)
	if err != nil {
		t.Fatal(err)
	}
	if snap.OpenFailure == nil || snap.OpenFailure.StepID != "s1" || snap.OpenFailure.Error != "boom" {
		t.Errorf("open failure = %+v", snap.OpenFailure)
	}
	if snap.Steps[0].Status != execute.StepFailed {
		t.Errorf("s1 status = %q, want failed", snap.Steps[0].Status)
	}
}

func TestReplayToSnapshot_FailureThenRetryClearsBanner(t *testing.T) {
	events := []Event{
		RunStartedEvent("p", 1, fixturePlan(), "slug"),
		StepStartedEvent("s1", "miyazaki"),
		StepFailedEvent("s1", "miyazaki", "boom"),
		StepStartedEvent("s1", "miyazaki"),
	}
	snap, err := EventsToSnapshot(events)
	if err != nil {
		t.Fatal(err)
	}
	if snap.OpenFailure != nil {
		t.Errorf("failure banner should clear on retry, got %+v", snap.OpenFailure)
	}
	if snap.Steps[0].Status != execute.StepRunning {
		t.Errorf("s1 status = %q, want running", snap.Steps[0].Status)
	}
}

func TestReplayToSnapshot_Terminal(t *testing.T) {
	events := []Event{
		RunStartedEvent("p", 1, fixturePlan(), "slug"),
		StepStartedEvent("s1", "miyazaki"),
		StepCompletedEvent("s1", "miyazaki", nil),
		StepStartedEvent("s2", "kaplan"),
		StepCompletedEvent("s2", "kaplan", nil),
		RunCompletedEvent(),
	}
	snap, err := EventsToSnapshot(events)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Terminal {
		t.Errorf("expected terminal snapshot")
	}
	if snap.OpenGate != nil || snap.OpenFailure != nil {
		t.Errorf("terminal snapshot should have no open gate/failure")
	}

	// Aborted variant.
	abortEvents := append(events[:len(events)-1], RunAbortedEvent("superseded by new run"))
	snap, err = EventsToSnapshot(abortEvents)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Terminal || snap.Reason != "superseded by new run" {
		t.Errorf("abort reason not captured: %+v", snap)
	}
}

func TestReplayToSnapshot_MissingHeaderIsError(t *testing.T) {
	_, err := EventsToSnapshot(nil)
	if err == nil {
		t.Error("expected error for empty events")
	}
	_, err = EventsToSnapshot([]Event{StepStartedEvent("s1", "a")})
	if err == nil {
		t.Error("expected error for header-less events")
	}
}

func TestReplayToSnapshot_PreservesStartedAt(t *testing.T) {
	plan := fixturePlan()
	head := RunStartedEvent("p", 1, plan, "slug")
	head.Timestamp = time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	snap, err := EventsToSnapshot([]Event{head})
	if err != nil {
		t.Fatal(err)
	}
	if !snap.StartedAt.Equal(head.Timestamp) {
		t.Errorf("started_at = %v, want %v", snap.StartedAt, head.Timestamp)
	}
}
