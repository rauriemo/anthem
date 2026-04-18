package execute

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/guests"
)

const (
	EventPlanLoaded    = "execution.plan_loaded"
	EventStepQueued    = "execution.step_queued"
	EventStepStarted   = "execution.step_started"
	EventStepCompleted = "execution.step_completed"
	EventStepFailed    = "execution.step_failed"
	EventGateOpened    = "execution.gate_opened"
	EventGateResolved  = "execution.gate_resolved"
	EventPlanCompleted = "execution.plan_completed"
	EventPlanAborted   = "execution.plan_aborted"
)

type planLoadedPayload struct {
	Title     string `json:"title"`
	StepCount int    `json:"step_count"`
	GateCount int    `json:"gate_count"`
}

type stepEventPayload struct {
	StepID      string         `json:"step_id"`
	AgentID     string         `json:"agent_id"`
	Description string         `json:"description,omitempty"`
	Artifacts   []StepArtifact `json:"artifacts,omitempty"`
	Error       string         `json:"error,omitempty"`
}

type gateEventPayload struct {
	GateID         string             `json:"gate_id"`
	Prompt         string             `json:"prompt,omitempty"`
	Artifacts      []StepArtifact     `json:"artifacts,omitempty"`
	AllowedActions []string           `json:"allowed_actions,omitempty"`
	Resolution     string             `json:"resolution,omitempty"`
	Feedback       string             `json:"feedback,omitempty"`
	AgentID        string             `json:"agent_id,omitempty"`
	AgentName      string             `json:"agent_name,omitempty"`
	StepID         string             `json:"step_id,omitempty"`
	PlanID         string             `json:"plan_id,omitempty"`
	ReviewLink     string             `json:"review_link,omitempty"`
	Review         *guests.ReviewSpec `json:"review,omitempty"`
}

// buildReviewLink returns a stable, shareable URL that opens the chain
// screen scrolled to the specified gate. Consumed by Prism's deep-link
// resolver (/chain?plan=<plan_id>&gate=<gate_id>) so external contexts
// (chat messages, emails) can link directly to an approval gate.
func buildReviewLink(planID, gateID string) string {
	if planID == "" && gateID == "" {
		return ""
	}
	q := url.Values{}
	if planID != "" {
		q.Set("plan", planID)
	}
	if gateID != "" {
		q.Set("gate", gateID)
	}
	return fmt.Sprintf("/chain?%s", q.Encode())
}

type planDonePayload struct {
	Title          string `json:"title"`
	TotalSteps     int    `json:"total_steps"`
	CompletedSteps int    `json:"completed_steps"`
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func PlanLoadedEvent(plan *ExecutionPlan, threadID string) channel.OutgoingMessage {
	return channel.OutgoingMessage{
		EventType: EventPlanLoaded,
		ThreadID:  threadID,
		Text: mustJSON(planLoadedPayload{
			Title:     plan.Metadata.Title,
			StepCount: len(plan.Steps),
			GateCount: len(plan.Gates),
		}),
	}
}

func StepQueuedEvent(step PlanStep, threadID string) channel.OutgoingMessage {
	return channel.OutgoingMessage{
		EventType: EventStepQueued,
		ThreadID:  threadID,
		Text: mustJSON(stepEventPayload{
			StepID:      step.ID,
			AgentID:     step.AgentID,
			Description: step.Description,
		}),
	}
}

func StepStartedEvent(step PlanStep, threadID string) channel.OutgoingMessage {
	return channel.OutgoingMessage{
		EventType: EventStepStarted,
		ThreadID:  threadID,
		Text: mustJSON(stepEventPayload{
			StepID:  step.ID,
			AgentID: step.AgentID,
		}),
	}
}

func StepCompletedEvent(stepID, agentID string, artifacts []StepArtifact, threadID string) channel.OutgoingMessage {
	return channel.OutgoingMessage{
		EventType: EventStepCompleted,
		ThreadID:  threadID,
		Text: mustJSON(stepEventPayload{
			StepID:    stepID,
			AgentID:   agentID,
			Artifacts: artifacts,
		}),
	}
}

func StepFailedEvent(stepID, agentID, errMsg string, threadID string) channel.OutgoingMessage {
	return channel.OutgoingMessage{
		EventType: EventStepFailed,
		ThreadID:  threadID,
		Text: mustJSON(stepEventPayload{
			StepID:  stepID,
			AgentID: agentID,
			Error:   errMsg,
		}),
	}
}

// GateOpenedEvent emits the event Prism uses to render the review drawer.
//
// agentID/agentName identify the guest that produced the gated artifacts so
// the drawer can title itself with the right persona and so chat
// notifications can be attributed correctly. review carries the agent's
// declared review spec (kind, panels, context files, etc.) straight from
// frontmatter. planID + gateID are the stable coordinates that power deep
// links (/chain?plan=<plan_id>&gate=<gate_id>) and let external contexts
// jump directly to an approval.
func GateOpenedEvent(
	gate ApprovalGate,
	artifacts []StepArtifact,
	stepID string,
	agentID string,
	agentName string,
	review *guests.ReviewSpec,
	planID string,
	threadID string,
) channel.OutgoingMessage {
	return channel.OutgoingMessage{
		EventType: EventGateOpened,
		ThreadID:  threadID,
		Text: mustJSON(gateEventPayload{
			GateID:         gate.ID,
			Prompt:         gate.Prompt,
			Artifacts:      artifacts,
			AllowedActions: []string{"approve", "revise", "abort"},
			AgentID:        agentID,
			AgentName:      agentName,
			StepID:         stepID,
			PlanID:         planID,
			ReviewLink:     buildReviewLink(planID, gate.ID),
			Review:         review,
		}),
	}
}

func GateResolvedEvent(gateID string, action GateAction, feedback string, threadID string) channel.OutgoingMessage {
	return channel.OutgoingMessage{
		EventType: EventGateResolved,
		ThreadID:  threadID,
		Text: mustJSON(gateEventPayload{
			GateID:     gateID,
			Resolution: string(action),
			Feedback:   feedback,
		}),
	}
}

func PlanCompletedEvent(plan *ExecutionPlan, threadID string) channel.OutgoingMessage {
	completed := 0
	for _, s := range plan.Steps {
		if s.Status == StepCompleted {
			completed++
		}
	}
	return channel.OutgoingMessage{
		EventType: EventPlanCompleted,
		ThreadID:  threadID,
		Text: mustJSON(planDonePayload{
			Title:          plan.Metadata.Title,
			TotalSteps:     len(plan.Steps),
			CompletedSteps: completed,
		}),
	}
}

func PlanAbortedEvent(plan *ExecutionPlan, reason string, threadID string) channel.OutgoingMessage {
	completed := 0
	for _, s := range plan.Steps {
		if s.Status == StepCompleted {
			completed++
		}
	}
	return channel.OutgoingMessage{
		EventType: EventPlanAborted,
		ThreadID:  threadID,
		Text: mustJSON(planDonePayload{
			Title:          plan.Metadata.Title,
			TotalSteps:     len(plan.Steps),
			CompletedSteps: completed,
		}),
	}
}
