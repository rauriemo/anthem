package runs

import (
	"encoding/json"

	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/execute"
)

// PlanSnapshotEvent builds the execution.plan_snapshot WebSocket
// message Prism consumes to hydrate the Execute view on connect. The
// payload is a Snapshot -- the replay-reduced projection of a run
// log -- plus a thread ID the client uses to route the message into
// its execution store.
//
// Keeping the builder in the runs package (rather than in the execute
// package next to the other event builders) avoids the import cycle:
// Snapshot itself carries execute types, and execute can't import
// runs because runs imports execute. Callers that already hold a
// Snapshot (the orchestrator on connect, startup replay) invoke this
// directly without additional glue.
func PlanSnapshotEvent(snap Snapshot, threadID string) channel.OutgoingMessage {
	return channel.OutgoingMessage{
		EventType: execute.EventPlanSnapshot,
		ThreadID:  threadID,
		Text:      mustJSON(snap),
	}
}

// mustJSON mirrors the helper in internal/execute/events.go so the
// snapshot builder doesn't drag in a cross-package dependency just to
// marshal a struct. Local to the package so the intent stays obvious
// at the call site.
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
