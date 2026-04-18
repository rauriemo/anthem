package execute

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/rauriemo/anthem/internal/channel"
	"github.com/rauriemo/anthem/internal/guests"
)

const (
	EventPlanLoaded       = "execution.plan_loaded"
	EventStepQueued       = "execution.step_queued"
	EventStepStarted      = "execution.step_started"
	EventStepCompleted    = "execution.step_completed"
	EventStepFailed       = "execution.step_failed"
	EventGateOpened       = "execution.gate_opened"
	EventGateResolved     = "execution.gate_resolved"
	EventGateNotification = "execution.gate_notification"
	EventPlanCompleted    = "execution.plan_completed"
	EventPlanAborted      = "execution.plan_aborted"
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

// gateNotificationPayload is the JSON shape Prism decodes when it receives
// an execution.gate_notification event. It is rendered as a guest-authored
// chat message (so it looks like the agent is telling the user "I need your
// review"), with a clickable link back to the approval gate and optional
// metadata for the Voice Gateway so the message can be spoken aloud in the
// finishing agent's voice.
type gateNotificationPayload struct {
	GateID     string `json:"gate_id"`
	StepID     string `json:"step_id"`
	PlanID     string `json:"plan_id,omitempty"`
	AgentID    string `json:"agent_id"`
	AgentName  string `json:"agent_name,omitempty"`
	VoiceID    string `json:"voice_id,omitempty"`
	VoiceModel string `json:"voice_model,omitempty"`
	Text       string `json:"text"`
	ReviewLink string `json:"review_link,omitempty"`
	// VoiceRequest signals the Voice Gateway to speak Text in the guest's
	// voice. Always true for gate_opened notifications; kept as an explicit
	// field so future causes (plan_aborted, step_failed) can opt out.
	VoiceRequest bool `json:"voice_request"`
	// Cause names the broader event that triggered this notification so
	// Prism can group related notifications in chat.
	Cause string `json:"cause"`
}

// GateNotificationEvent builds a guest-authored chat event announcing a
// newly-opened approval gate. The runner broadcasts this immediately before
// GateOpenedEvent so the chat panel and the chain drawer update in the same
// tick, and the finishing agent's voice can announce the gate without the
// orchestrator appearing to author the message.
//
// If review carries a NotificationTemplate the runner-supplied vars
// ({agent_name}, {step_title}, {artifact_count}) are substituted in; when
// the template is empty a sensible default is used.
func GateNotificationEvent(
	gate ApprovalGate,
	stepID string,
	stepTitle string,
	artifactCount int,
	agent guests.GuestAgent,
	review *guests.ReviewSpec,
	planID string,
	threadID string,
) channel.OutgoingMessage {
	tpl := ""
	if review != nil {
		tpl = review.NotificationTemplate
	}
	text := renderNotificationTemplate(tpl, agent.Name, stepTitle, artifactCount)
	return channel.OutgoingMessage{
		EventType: EventGateNotification,
		ThreadID:  threadID,
		GuestID:   agent.ID,
		Text: mustJSON(gateNotificationPayload{
			GateID:       gate.ID,
			StepID:       stepID,
			PlanID:       planID,
			AgentID:      agent.ID,
			AgentName:    agent.Name,
			VoiceID:      agent.VoiceID,
			VoiceModel:   agent.VoiceModel,
			Text:         text,
			ReviewLink:   buildReviewLink(planID, gate.ID),
			VoiceRequest: true,
			Cause:        "gate_opened",
		}),
	}
}

// renderNotificationTemplate substitutes {agent_name}, {step_title}, and
// {artifact_count} in tpl. If tpl is empty, returns a sensible default.
func renderNotificationTemplate(tpl, agentName, stepTitle string, artifactCount int) string {
	if strings.TrimSpace(tpl) == "" {
		if stepTitle == "" {
			return fmt.Sprintf("I've finished and need your review (%d artifact(s)).", artifactCount)
		}
		return fmt.Sprintf("I've finished %q and need your review (%d artifact(s)).", stepTitle, artifactCount)
	}
	out := tpl
	out = strings.ReplaceAll(out, "{agent_name}", agentName)
	out = strings.ReplaceAll(out, "{step_title}", stepTitle)
	out = strings.ReplaceAll(out, "{artifact_count}", fmt.Sprintf("%d", artifactCount))
	return out
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
