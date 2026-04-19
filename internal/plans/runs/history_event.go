package runs

import (
	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/execute"
)

// runHistoryPayload is the JSON shape Prism decodes on
// execution.run_history. Wrapping the slice in an object (rather than
// emitting a bare array) keeps the payload forward-compatible: we can
// add `truncated_at`, `project_slug`, pagination fields, etc. without
// a wire break.
type runHistoryPayload struct {
	Runs []Snapshot `json:"runs"`
}

// RunHistoryEvent builds the execution.run_history WebSocket message
// that populates Prism's scrollable Execute history on connect or in
// response to a client-initiated hydrate frame. The snaps slice is
// expected to be newest-first and already bounded by the caller's
// retention cap (typically runs.ListAll).
//
// A nil snaps slice produces an empty-but-well-formed payload
// ({"runs":[]}) so the frontend always sees a deterministic shape and
// can unconditionally replace its run list on receipt.
func RunHistoryEvent(snaps []Snapshot, threadID string) channel.OutgoingMessage {
	if snaps == nil {
		snaps = []Snapshot{}
	}
	return channel.OutgoingMessage{
		EventType: execute.EventRunHistory,
		ThreadID:  threadID,
		Text:      mustJSON(runHistoryPayload{Runs: snaps}),
	}
}

// RunArchivedEventFromSnapshot builds execution.run_archived by
// shipping the full Snapshot verbatim. Used by callers that already
// hold a replay-reduced Snapshot (e.g. the orchestrator on startup
// triage). The runner emits the lighter-weight marker variant
// defined in execute/events.go (execute.RunArchivedEvent) because
// building a Snapshot from inside the runner would require importing
// the runs package and introduce a cycle.
//
// Both variants land on the same wire event type; the frontend
// matches on plan_id + compile_generation and flips the matching
// history entry's terminal flag without caring which builder
// produced the payload.
func RunArchivedEventFromSnapshot(snap Snapshot, threadID string) channel.OutgoingMessage {
	return channel.OutgoingMessage{
		EventType: execute.EventRunArchived,
		ThreadID:  threadID,
		Text:      mustJSON(snap),
	}
}
