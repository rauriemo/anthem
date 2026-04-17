package backend

import (
	"context"

	"github.com/rauriemo/anthem/internal/types"
)

// BackendEvent is emitted by an ExecutionBackend to report lifecycle progress.
type BackendEvent struct {
	Type   string
	TaskID string
	Data   any
}

// ExecutionBackend wraps the "find work, dispatch, track, retry" lifecycle.
// Implementations are opt-in components that the orchestrator starts when
// configured (e.g., a GitHub issue tracker).
type ExecutionBackend interface {
	Start(ctx context.Context) error
	Stop()
	QueueWork(items []types.Task) error
	ActiveWork() []types.Task
	OnProgress(callback func(event BackendEvent))
}

// LoopHost is the callback interface the tick-based backends use to drive
// the orchestrator on each polling cycle. The orchestrator implements this.
type LoopHost interface {
	Tick(ctx context.Context)
	PollingIntervalMS() int
}
