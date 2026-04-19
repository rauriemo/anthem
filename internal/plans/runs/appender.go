package runs

import (
	"log/slog"

	"github.com/rauriemo/anthem/internal/execute"
	"github.com/rauriemo/anthem/internal/guests"
)

// FileAppender implements execute.RunEventAppender by translating each
// runner transition into an Event and appending it to a .jsonl run log.
// A single instance is bound to one run at construction time; a new
// instance is allocated per dispatch by the orchestrator so each run
// gets its own file.
//
// Append failures are logged but not surfaced to the runner -- the
// runner's broadcast() path has the same "log and continue"
// discipline, and escalating a single missed append to a run-abort
// would be worse than a minor durability gap.
type FileAppender struct {
	path   string
	logger *slog.Logger
}

// NewFileAppender returns an appender that writes to runPath. logger
// is optional; when nil, slog.Default is used. The path is not
// validated here; the first Append call materializes the directory
// and opens the file with O_APPEND|O_CREATE.
func NewFileAppender(runPath string, logger *slog.Logger) *FileAppender {
	if logger == nil {
		logger = slog.Default()
	}
	return &FileAppender{path: runPath, logger: logger}
}

// Path exposes the underlying run log path so callers (orchestrator,
// tests) can emit snapshots, open the file for replay, or surface it
// in diagnostics.
func (a *FileAppender) Path() string {
	return a.path
}

func (a *FileAppender) append(ev Event) {
	if err := Append(a.path, ev); err != nil {
		a.logger.Warn("runs: append failed", "type", ev.Type, "path", a.path, "error", err)
	}
}

func (a *FileAppender) AppendRunStarted(planID string, compileGen int, plan *execute.ExecutionPlan, projectSlug string) {
	a.append(RunStartedEvent(planID, compileGen, plan, projectSlug))
}

func (a *FileAppender) AppendStepQueued(stepID, agentID string) {
	a.append(StepQueuedEvent(stepID, agentID))
}

func (a *FileAppender) AppendStepStarted(stepID, agentID string) {
	a.append(StepStartedEvent(stepID, agentID))
}

func (a *FileAppender) AppendStepCompleted(stepID, agentID string, artifacts []execute.StepArtifact) {
	a.append(StepCompletedEvent(stepID, agentID, artifacts))
}

func (a *FileAppender) AppendStepFailed(stepID, agentID, errMsg string) {
	a.append(StepFailedEvent(stepID, agentID, errMsg))
}

func (a *FileAppender) AppendGateOpened(
	gateID, stepID, agentID, agentName, prompt string,
	artifacts []execute.StepArtifact,
	allowedActions []string,
	review *guests.ReviewSpec,
	reviewLink string,
) {
	a.append(GateOpenedEvent(gateID, stepID, agentID, agentName, prompt, artifacts, allowedActions, review, reviewLink))
}

func (a *FileAppender) AppendGateResolved(gateID string, action execute.GateAction, feedback string) {
	a.append(GateResolvedEvent(gateID, action, feedback))
}

func (a *FileAppender) AppendRunCompleted() {
	a.append(RunCompletedEvent())
}

func (a *FileAppender) AppendRunAborted(reason string) {
	a.append(RunAbortedEvent(reason))
}
