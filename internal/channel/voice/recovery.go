package voice

import (
	"fmt"
	"strings"
	"sync"
)

// InterruptionContext captures the state at the point of a barge-in so the
// next prompt can include it for context continuity.
type InterruptionContext struct {
	PartialResponse string
	ThreadID        string
	AgentID         string
	TurnIndex       int
}

// InterruptionRecovery manages context continuity across barge-in interruptions.
// When the user interrupts, it captures the partial response and injects a
// recovery prefix into the next prompt so the orchestrator can resume coherently.
type InterruptionRecovery struct {
	mu        sync.Mutex
	last      *InterruptionContext
	turnIndex int
}

func NewInterruptionRecovery() *InterruptionRecovery {
	return &InterruptionRecovery{}
}

// Capture records an interruption's partial context.
func (ir *InterruptionRecovery) Capture(partialResponse, threadID, agentID string) {
	ir.mu.Lock()
	defer ir.mu.Unlock()
	ir.turnIndex++
	ir.last = &InterruptionContext{
		PartialResponse: partialResponse,
		ThreadID:        threadID,
		AgentID:         agentID,
		TurnIndex:       ir.turnIndex,
	}
}

// RecoveryPrefix returns a prompt prefix for the next turn that includes the
// interrupted context, or empty string if there was no recent interruption.
func (ir *InterruptionRecovery) RecoveryPrefix() string {
	ir.mu.Lock()
	defer ir.mu.Unlock()

	if ir.last == nil {
		return ""
	}

	ctx := ir.last
	ir.last = nil

	partial := strings.TrimSpace(ctx.PartialResponse)
	if partial == "" {
		return ""
	}

	return fmt.Sprintf(
		"[The user interrupted. Your previous response was cut off at: \"%s\". "+
			"Continue naturally from where you left off, acknowledging the interruption if relevant.]",
		truncate(partial, 200),
	)
}

// HasPending reports whether there is an unresolved interruption context.
func (ir *InterruptionRecovery) HasPending() bool {
	ir.mu.Lock()
	defer ir.mu.Unlock()
	return ir.last != nil
}

// Clear discards any pending interruption context.
func (ir *InterruptionRecovery) Clear() {
	ir.mu.Lock()
	defer ir.mu.Unlock()
	ir.last = nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
