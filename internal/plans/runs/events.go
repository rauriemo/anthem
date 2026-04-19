// Package runs owns the per-dispatch append-only run log that sits next to
// each markdown plan file. One .jsonl per dispatch captures every
// PlanRunner state transition as an event, making execution runs as
// durable as the plan definition itself.
//
// File layout:
//
//	~/.anthem/plans/{project-slug}/
//	  <plan>.plan.md
//	  <plan>.plan.md.execute.json
//	  <plan>.plan.md.runs/
//	    run_<iso>_gen<N>.jsonl        <- one file per dispatch
//
// Event stream is strictly append-only. Every event type carries a
// timestamp plus the fields the orchestrator/PlanRunner needs to rebuild
// state on replay.
package runs

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rauriemo/anthem/internal/execute"
	"github.com/rauriemo/anthem/internal/guests"
)

// EventType enumerates the discrete transitions that appear in a run log.
// The string values are stable and form the wire contract when logs are
// replayed into a Snapshot. Callers outside this package should never
// construct events directly; use the helper constructors below instead.
type EventType string

const (
	EventRunStarted    EventType = "run_started"
	EventStepQueued    EventType = "step_queued"
	EventStepStarted   EventType = "step_started"
	EventStepCompleted EventType = "step_completed"
	EventStepFailed    EventType = "step_failed"
	EventGateOpened    EventType = "gate_opened"
	EventGateResolved  EventType = "gate_resolved"
	EventRunCompleted  EventType = "run_completed"
	EventRunAborted    EventType = "run_aborted"
)

// Event is a single line in a run log. Only the fields relevant to a
// given Type are populated; the rest are omitted from JSON so the log
// lines stay compact and diff-friendly. All timestamps are UTC.
//
// The shape is deliberately flat rather than a discriminated union so
// logs remain human-readable and jq-queryable without custom codecs.
type Event struct {
	Type      EventType `json:"type"`
	Timestamp time.Time `json:"ts"`

	// run_started
	PlanID            string   `json:"plan_id,omitempty"`
	CompileGeneration int      `json:"compile_generation,omitempty"`
	Title             string   `json:"title,omitempty"`
	Description       string   `json:"description,omitempty"`
	RequiredProfiles  []string `json:"required_profiles,omitempty"`
	ProjectSlug       string   `json:"project_slug,omitempty"`
	// PlanSteps/PlanGates capture the compiled plan shape at run start so
	// a replay can reconstruct the step list and gate attachments without
	// reading the compiled sidecar. This makes Snapshot self-contained
	// and resilient to subsequent recompiles.
	PlanSteps []execute.PlanStep     `json:"plan_steps,omitempty"`
	PlanGates []execute.ApprovalGate `json:"plan_gates,omitempty"`

	// step_* + gate_*
	StepID    string                 `json:"step_id,omitempty"`
	AgentID   string                 `json:"agent_id,omitempty"`
	AgentName string                 `json:"agent_name,omitempty"`
	Artifacts []execute.StepArtifact `json:"artifacts,omitempty"`
	Error     string                 `json:"error,omitempty"`

	// gate_opened
	GateID         string             `json:"gate_id,omitempty"`
	Prompt         string             `json:"prompt,omitempty"`
	AllowedActions []string           `json:"allowed_actions,omitempty"`
	Review         *guests.ReviewSpec `json:"review,omitempty"`
	ReviewLink     string             `json:"review_link,omitempty"`

	// gate_resolved
	Resolution string `json:"resolution,omitempty"`
	Feedback   string `json:"feedback,omitempty"`

	// run_aborted
	Reason string `json:"reason,omitempty"`
}

// RunStartedEvent builds an Event for the start of a new dispatch. The
// plan parameter is a full ExecutionPlan snapshot so replay can
// reconstruct step/gate identities without a second file read.
func RunStartedEvent(planID string, compileGen int, plan *execute.ExecutionPlan, projectSlug string) Event {
	title, description := "", ""
	if plan != nil {
		title = plan.Metadata.Title
		description = plan.Metadata.Description
	}
	steps := make([]execute.PlanStep, 0)
	gates := make([]execute.ApprovalGate, 0)
	if plan != nil {
		steps = append(steps, plan.Steps...)
		gates = append(gates, plan.Gates...)
	}
	return Event{
		Type:              EventRunStarted,
		Timestamp:         time.Now().UTC(),
		PlanID:            planID,
		CompileGeneration: compileGen,
		Title:             title,
		Description:       description,
		ProjectSlug:       projectSlug,
		PlanSteps:         steps,
		PlanGates:         gates,
	}
}

// StepQueuedEvent records a step transitioning to StepQueued. Rare in
// practice (the current runner goes straight to Running) but supported
// for future dependency-driven scheduling.
func StepQueuedEvent(stepID, agentID string) Event {
	return Event{
		Type:      EventStepQueued,
		Timestamp: time.Now().UTC(),
		StepID:    stepID,
		AgentID:   agentID,
	}
}

// StepStartedEvent records the transition to StepRunning for a given
// step. Emitted by PlanRunner.runStep before the agent is dispatched.
func StepStartedEvent(stepID, agentID string) Event {
	return Event{
		Type:      EventStepStarted,
		Timestamp: time.Now().UTC(),
		StepID:    stepID,
		AgentID:   agentID,
	}
}

// StepCompletedEvent records successful completion of a step with its
// collected artifacts. The artifacts slice is stored verbatim so a
// replay can rebuild the runner's per-step artifact map.
func StepCompletedEvent(stepID, agentID string, artifacts []execute.StepArtifact) Event {
	return Event{
		Type:      EventStepCompleted,
		Timestamp: time.Now().UTC(),
		StepID:    stepID,
		AgentID:   agentID,
		Artifacts: append([]execute.StepArtifact(nil), artifacts...),
	}
}

// StepFailedEvent records a step failure with the collected error
// message. A failure does not terminate the run; the runner pauses
// waiting for user resolution via ResolveFailure.
func StepFailedEvent(stepID, agentID, errMsg string) Event {
	return Event{
		Type:      EventStepFailed,
		Timestamp: time.Now().UTC(),
		StepID:    stepID,
		AgentID:   agentID,
		Error:     errMsg,
	}
}

// GateOpenedEvent records an approval gate reaching the user with its
// full review payload. The payload is embedded verbatim so a hydration
// on reconnect (or a cold replay the next morning) renders the review
// drawer identically to the original gate_opened broadcast.
func GateOpenedEvent(
	gateID string,
	stepID string,
	agentID string,
	agentName string,
	prompt string,
	artifacts []execute.StepArtifact,
	allowedActions []string,
	review *guests.ReviewSpec,
	reviewLink string,
) Event {
	return Event{
		Type:           EventGateOpened,
		Timestamp:      time.Now().UTC(),
		GateID:         gateID,
		StepID:         stepID,
		AgentID:        agentID,
		AgentName:      agentName,
		Prompt:         prompt,
		Artifacts:      append([]execute.StepArtifact(nil), artifacts...),
		AllowedActions: append([]string(nil), allowedActions...),
		Review:         review,
		ReviewLink:     reviewLink,
	}
}

// GateResolvedEvent records the user's resolution of a pending gate.
// The resolution value is a GateAction string ("approve", "revise",
// "abort") and feedback carries any revision text.
func GateResolvedEvent(gateID string, resolution execute.GateAction, feedback string) Event {
	return Event{
		Type:       EventGateResolved,
		Timestamp:  time.Now().UTC(),
		GateID:     gateID,
		Resolution: string(resolution),
		Feedback:   feedback,
	}
}

// RunCompletedEvent marks a successful end-to-end run. No additional
// fields are needed; the timestamp is the completion marker used by
// ListActive to filter terminal runs.
func RunCompletedEvent() Event {
	return Event{
		Type:      EventRunCompleted,
		Timestamp: time.Now().UTC(),
	}
}

// RunAbortedEvent records a run ending without completing every step.
// The reason string is rendered in Prism so users understand whether
// the abort was user-initiated, a step failure, a crash-recovery
// decision, or a supersede.
func RunAbortedEvent(reason string) Event {
	return Event{
		Type:      EventRunAborted,
		Timestamp: time.Now().UTC(),
		Reason:    reason,
	}
}

// IsTerminal reports whether an event ends the run. ListActive filters
// on the tail event; a run whose last line is terminal is no longer
// considered active.
func (e Event) IsTerminal() bool {
	return e.Type == EventRunCompleted || e.Type == EventRunAborted
}

// marshal serializes an event to a single line of JSON terminated with a
// newline so append writes are atomic on POSIX filesystems.
func (e Event) marshal() ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("runs: marshal event: %w", err)
	}
	return append(b, '\n'), nil
}
