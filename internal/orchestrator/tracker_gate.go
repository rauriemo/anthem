package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
)

// ErrTrackerMutationDeniedInMode is returned by modeGatedTracker's
// mutating methods when the orchestrator's current mode is anything
// other than ModeLoop. It is the sentinel clients test against when
// they want to distinguish "mode-denied" from a real tracker error
// (the two are semantically different -- mode denial is a guardrail,
// a tracker error is an infra failure).
var ErrTrackerMutationDeniedInMode = errors.New(
	"tracker mutations are loop-only; chat/plan/execute routes cannot start, update, or label issues",
)

// modeGatedTracker wraps a tracker.IssueTracker so every mutating
// method fails fast with ErrTrackerMutationDeniedInMode when the
// orchestrator's CurrentMode is anything other than ModeLoop. Read
// methods (ListActive, GetTask, ShouldThrottle) pass through
// unchanged so Chat/Plan/Execute can still observe tracker state.
//
// This is Layer 3 of the three-layer route-isolation boundary:
//
//   - Layer 1: IsLoopOnlyAction + executeActions dispatch gate in
//     contract.go / orchestrator.go -- blocks the Go-side action
//     dispatch of ActionCreateSubtasks et al.
//   - Layer 2: modeGatedRunner (runner_gate.go) -- blocks Claude
//     from shelling out to gh/curl/WebFetch.
//   - Layer 3: modeGatedTracker (this file) -- blocks every direct
//     Go-code path to tracker.* mutation, current and future.
//
// The facade is wrapped once in Orchestrator.New so every existing
// and future caller inherits the invariant without touching each
// call site. See orchestrator.New for the wire-up.
type modeGatedTracker struct {
	inner tracker.IssueTracker
	mode  func() types.Mode
}

// newModeGatedTracker wraps inner with a mode gate. The mode fn is
// evaluated on every call so mode transitions during the life of
// the orchestrator (chat -> plan -> execute -> loop) take effect
// immediately with no re-wrap.
func newModeGatedTracker(inner tracker.IssueTracker, mode func() types.Mode) *modeGatedTracker {
	return &modeGatedTracker{inner: inner, mode: mode}
}

// ListActive passes through in every mode. Reading active issues
// is how Chat/Plan/Execute surfaces context -- it must not be
// gated.
func (t *modeGatedTracker) ListActive(ctx context.Context) ([]types.Task, error) {
	return t.inner.ListActive(ctx)
}

// GetTask passes through in every mode.
func (t *modeGatedTracker) GetTask(ctx context.Context, id string) (*types.Task, error) {
	return t.inner.GetTask(ctx, id)
}

// ShouldThrottle passes through in every mode. Throttling is a
// rate-limit signal, not a mutation.
func (t *modeGatedTracker) ShouldThrottle() (bool, time.Duration) {
	return t.inner.ShouldThrottle()
}

// UpdateStatus is Loop-only.
func (t *modeGatedTracker) UpdateStatus(ctx context.Context, id string, status string) error {
	if t.mode() != types.ModeLoop {
		return ErrTrackerMutationDeniedInMode
	}
	return t.inner.UpdateStatus(ctx, id, status)
}

// AddComment is Loop-only.
func (t *modeGatedTracker) AddComment(ctx context.Context, id string, body string) error {
	if t.mode() != types.ModeLoop {
		return ErrTrackerMutationDeniedInMode
	}
	return t.inner.AddComment(ctx, id, body)
}

// AddLabel is Loop-only.
func (t *modeGatedTracker) AddLabel(ctx context.Context, id string, label string) error {
	if t.mode() != types.ModeLoop {
		return ErrTrackerMutationDeniedInMode
	}
	return t.inner.AddLabel(ctx, id, label)
}

// RemoveLabel is Loop-only.
func (t *modeGatedTracker) RemoveLabel(ctx context.Context, id string, label string) error {
	if t.mode() != types.ModeLoop {
		return ErrTrackerMutationDeniedInMode
	}
	return t.inner.RemoveLabel(ctx, id, label)
}

// CreateIssue is Loop-only. This is the method responsible for the
// "agent silently created 9 GitHub issues in Execute mode"
// regression -- closing it at the interface boundary means any
// future refactor that reaches for o.tracker.CreateIssue outside
// executeActions is also caught.
func (t *modeGatedTracker) CreateIssue(ctx context.Context, title string, body string, labels []string) (string, error) {
	if t.mode() != types.ModeLoop {
		return "", ErrTrackerMutationDeniedInMode
	}
	return t.inner.CreateIssue(ctx, title, body, labels)
}
