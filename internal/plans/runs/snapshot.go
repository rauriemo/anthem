package runs

import (
	"fmt"
	"time"

	"github.com/rauriemo/anthem/internal/execute"
	"github.com/rauriemo/anthem/internal/guests"
)

// Snapshot is the derived, replay-reduced view of a run log. It is
// what the orchestrator emits to Prism as an execution.plan_snapshot
// and what PlanRunner startup replay uses to reconstruct in-memory
// state for a run parked at a gate.
//
// The shape intentionally mirrors planLoadedPayload extended with
// per-step status, per-step artifacts, and any open gate or failure
// so the Prism executionStore can hydrate in a single atomic call.
type Snapshot struct {
	PlanID            string                `json:"plan_id"`
	CompileGeneration int                   `json:"compile_generation"`
	Title             string                `json:"title"`
	Description       string                `json:"description,omitempty"`
	RequiredProfiles  []string              `json:"required_profiles,omitempty"`
	ProjectSlug       string                `json:"project_slug,omitempty"`
	StartedAt         time.Time             `json:"started_at"`

	Steps     []SnapshotStep     `json:"steps"`
	Artifacts map[string][]execute.StepArtifact `json:"-"`

	OpenGate    *SnapshotGate    `json:"open_gate,omitempty"`
	OpenFailure *SnapshotFailure `json:"open_failure,omitempty"`

	// Terminal is true when the tail event is run_completed or
	// run_aborted. A terminal snapshot is surfaced only for debug /
	// post-mortem paths; the orchestrator filters it out before emit.
	Terminal bool   `json:"terminal,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// SnapshotStep mirrors planStepSummary with the runtime status and any
// collected artifacts folded in. This is what ExecuteProgressView
// renders on the Prism side.
type SnapshotStep struct {
	ID        string                 `json:"id"`
	AgentID   string                 `json:"agent_id"`
	Label     string                 `json:"label,omitempty"`
	DependsOn string                 `json:"depends_on,omitempty"`
	Status    execute.StepStatus     `json:"status"`
	Artifacts []execute.StepArtifact `json:"artifacts,omitempty"`
}

// SnapshotGate is the open-gate projection: exactly the data Prism
// needs to render the review drawer as though gate_opened had just
// arrived.
type SnapshotGate struct {
	ID             string                 `json:"id"`
	StepID         string                 `json:"step_id"`
	AgentID        string                 `json:"agent_id,omitempty"`
	AgentName      string                 `json:"agent_name,omitempty"`
	Prompt         string                 `json:"prompt,omitempty"`
	Artifacts      []execute.StepArtifact `json:"artifacts,omitempty"`
	AllowedActions []string               `json:"allowed_actions,omitempty"`
	Review         *guests.ReviewSpec     `json:"review,omitempty"`
	ReviewLink     string                 `json:"review_link,omitempty"`
	OpenedAt       time.Time              `json:"opened_at"`
}

// SnapshotFailure is the open-failure projection for a run paused on a
// StepFailed event. Prism renders this as a retry/revise/abort
// affordance, mirroring the runner's handleStepFailure loop.
type SnapshotFailure struct {
	StepID   string    `json:"step_id"`
	AgentID  string    `json:"agent_id,omitempty"`
	Error    string    `json:"error,omitempty"`
	FailedAt time.Time `json:"failed_at"`
}

// ReplayToSnapshot reduces a run log into its current Snapshot. Events
// are applied in order; a later event overrides an earlier one on the
// same step or gate. If the log has no run_started header the function
// returns an error -- a header-less log is corrupt and shouldn't be
// fed into startup replay or snapshot emission.
func ReplayToSnapshot(runPath string) (Snapshot, error) {
	events, err := Replay(runPath)
	if err != nil {
		return Snapshot{}, err
	}
	return EventsToSnapshot(events)
}

// EventsToSnapshot is the pure reduction step that ReplayToSnapshot
// layers atop Replay. Exposed so tests and future in-process consumers
// can fold events without a file.
func EventsToSnapshot(events []Event) (Snapshot, error) {
	if len(events) == 0 {
		return Snapshot{}, fmt.Errorf("runs: empty event stream")
	}
	head := events[0]
	if head.Type != EventRunStarted {
		return Snapshot{}, fmt.Errorf("runs: first event is %q, expected run_started", head.Type)
	}

	snap := Snapshot{
		PlanID:            head.PlanID,
		CompileGeneration: head.CompileGeneration,
		Title:             head.Title,
		Description:       head.Description,
		RequiredProfiles:  append([]string(nil), head.RequiredProfiles...),
		ProjectSlug:       head.ProjectSlug,
		StartedAt:         head.Timestamp,
		Artifacts:         make(map[string][]execute.StepArtifact, len(head.PlanSteps)),
	}

	// Initialize steps from the recorded plan shape. Every step starts
	// Pending; later events promote them to Running/Completed/Failed.
	snap.Steps = make([]SnapshotStep, 0, len(head.PlanSteps))
	stepIdx := make(map[string]int, len(head.PlanSteps))
	for _, s := range head.PlanSteps {
		stepIdx[s.ID] = len(snap.Steps)
		snap.Steps = append(snap.Steps, SnapshotStep{
			ID:        s.ID,
			AgentID:   s.AgentID,
			Label:     s.Description,
			DependsOn: s.DependsOn,
			Status:    execute.StepPending,
		})
	}

	for _, e := range events[1:] {
		applyEvent(&snap, stepIdx, e)
	}

	return snap, nil
}

// applyEvent folds a single event into the evolving Snapshot. The
// function is deliberately monolithic (one switch) rather than a map
// of handlers so the intent of each transition is obvious at the call
// site and tests can reason about ordering linearly.
func applyEvent(snap *Snapshot, stepIdx map[string]int, e Event) {
	switch e.Type {
	case EventStepQueued:
		if i, ok := stepIdx[e.StepID]; ok {
			snap.Steps[i].Status = execute.StepPending
		}
		clearOpenFailureFor(snap, e.StepID)
	case EventStepStarted:
		if i, ok := stepIdx[e.StepID]; ok {
			snap.Steps[i].Status = execute.StepRunning
		}
		clearOpenFailureFor(snap, e.StepID)
	case EventStepCompleted:
		if i, ok := stepIdx[e.StepID]; ok {
			snap.Steps[i].Status = execute.StepCompleted
			snap.Steps[i].Artifacts = append([]execute.StepArtifact(nil), e.Artifacts...)
		}
		snap.Artifacts[e.StepID] = append([]execute.StepArtifact(nil), e.Artifacts...)
		clearOpenFailureFor(snap, e.StepID)
	case EventStepFailed:
		if i, ok := stepIdx[e.StepID]; ok {
			snap.Steps[i].Status = execute.StepFailed
		}
		snap.OpenFailure = &SnapshotFailure{
			StepID:   e.StepID,
			AgentID:  e.AgentID,
			Error:    e.Error,
			FailedAt: e.Timestamp,
		}
	case EventGateOpened:
		snap.OpenGate = &SnapshotGate{
			ID:             e.GateID,
			StepID:         e.StepID,
			AgentID:        e.AgentID,
			AgentName:      e.AgentName,
			Prompt:         e.Prompt,
			Artifacts:      append([]execute.StepArtifact(nil), e.Artifacts...),
			AllowedActions: append([]string(nil), e.AllowedActions...),
			Review:         e.Review,
			ReviewLink:     e.ReviewLink,
			OpenedAt:       e.Timestamp,
		}
	case EventGateResolved:
		// Revise/abort/approve all close the gate; downstream events
		// (step re-queue on revise, run_aborted on abort) handle the
		// knock-on state. Capture the pre-close step ID before nilling
		// so the revise branch can flip it back to Pending.
		var gateStepID string
		if snap.OpenGate != nil && snap.OpenGate.ID == e.GateID {
			gateStepID = snap.OpenGate.StepID
			snap.OpenGate = nil
		}
		if e.Resolution == string(execute.GateRevise) && gateStepID != "" {
			if i, ok := stepIdx[gateStepID]; ok {
				snap.Steps[i].Status = execute.StepPending
			}
		}
	case EventRunCompleted:
		snap.Terminal = true
		snap.OpenGate = nil
		snap.OpenFailure = nil
	case EventRunAborted:
		snap.Terminal = true
		snap.Reason = e.Reason
		snap.OpenGate = nil
		snap.OpenFailure = nil
	}
}

// clearOpenFailureFor nils the open failure projection if it refers to
// the given step -- a retry/revise/requeue of the failed step means
// Prism should stop showing the failure banner.
func clearOpenFailureFor(snap *Snapshot, stepID string) {
	if snap.OpenFailure != nil && snap.OpenFailure.StepID == stepID {
		snap.OpenFailure = nil
	}
}
