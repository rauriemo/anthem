package execute

import "github.com/rauriemo/anthem/internal/guests"

// RunEventAppender receives every PlanRunner state transition in
// arrival order. Implementations are expected to persist each call
// before returning so downstream broadcasts and in-memory cache
// updates are never ahead of the durable log. The runner treats
// appender errors as soft (logged, not fatal) to match the existing
// broadcast() semantics; implementations that need strict guarantees
// should surface errors out-of-band (logs, metrics) and arrange for
// the operator to intervene.
//
// The interface lives in the execute package so the runner has no
// dependency on plans/runs (which imports execute for step/gate
// types). The production implementation lives in
// internal/plans/runs.FileAppender and is wired by the orchestrator.
// Tests use NoopAppender or a fake that records calls.
type RunEventAppender interface {
	AppendRunStarted(planID string, compileGen int, plan *ExecutionPlan, projectSlug string)
	AppendStepQueued(stepID, agentID string)
	AppendStepStarted(stepID, agentID string)
	AppendStepCompleted(stepID, agentID string, artifacts []StepArtifact)
	AppendStepFailed(stepID, agentID, errMsg string)
	AppendGateOpened(
		gateID, stepID, agentID, agentName, prompt string,
		artifacts []StepArtifact,
		allowedActions []string,
		review *guests.ReviewSpec,
		reviewLink string,
	)
	AppendGateResolved(gateID string, action GateAction, feedback string)
	AppendRunCompleted()
	AppendRunAborted(reason string)
}

// NoopAppender is the default RunEventAppender when the orchestrator
// does not provide one (tests, legacy paths via tryLoadExecutionPlan).
// It discards every call so the runner can remain log-first in its
// code path without requiring a file path for every invocation.
type NoopAppender struct{}

func (NoopAppender) AppendRunStarted(string, int, *ExecutionPlan, string) {}
func (NoopAppender) AppendStepQueued(string, string)                      {}
func (NoopAppender) AppendStepStarted(string, string)                     {}
func (NoopAppender) AppendStepCompleted(string, string, []StepArtifact)   {}
func (NoopAppender) AppendStepFailed(string, string, string)              {}
func (NoopAppender) AppendGateOpened(string, string, string, string, string, []StepArtifact, []string, *guests.ReviewSpec, string) {
}
func (NoopAppender) AppendGateResolved(string, GateAction, string) {}
func (NoopAppender) AppendRunCompleted()                           {}
func (NoopAppender) AppendRunAborted(string)                       {}
