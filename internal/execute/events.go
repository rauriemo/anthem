package execute

import (
	"encoding/json"

	"github.com/rauriemo/anthem/internal/channel"
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
	GateID         string         `json:"gate_id"`
	Prompt         string         `json:"prompt,omitempty"`
	Artifacts      []StepArtifact `json:"artifacts,omitempty"`
	AllowedActions []string       `json:"allowed_actions,omitempty"`
	Resolution     string         `json:"resolution,omitempty"`
	Feedback       string         `json:"feedback,omitempty"`
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

func GateOpenedEvent(gate ApprovalGate, artifacts []StepArtifact, threadID string) channel.OutgoingMessage {
	return channel.OutgoingMessage{
		EventType: EventGateOpened,
		ThreadID:  threadID,
		Text: mustJSON(gateEventPayload{
			GateID:         gate.ID,
			Prompt:         gate.Prompt,
			Artifacts:      artifacts,
			AllowedActions: []string{"approve", "revise", "abort"},
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
