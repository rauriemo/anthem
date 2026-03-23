package orchestrator

import (
	"errors"
	"fmt"
	"slices"
)

// RiskLevel classifies how dangerous an action is.
type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

// ActionType identifies a contract action the orchestrator can propose.
type ActionType string

const (
	ActionDispatch         ActionType = "dispatch"
	ActionSkip             ActionType = "skip"
	ActionComment          ActionType = "comment"
	ActionUpdateVoice      ActionType = "update_voice"
	ActionRequestApproval  ActionType = "request_approval"
	ActionCloseWave        ActionType = "close_wave"
	ActionCreateSubtasks   ActionType = "create_subtasks"
	ActionPromoteKnowledge ActionType = "promote_knowledge"
)

// allActionTypes enumerates every known action type for validation.
var allActionTypes = []ActionType{
	ActionDispatch,
	ActionSkip,
	ActionComment,
	ActionUpdateVoice,
	ActionRequestApproval,
	ActionCloseWave,
	ActionCreateSubtasks,
	ActionPromoteKnowledge,
}

// SubtaskDef defines a subtask to be created by the create_subtasks action.
type SubtaskDef struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels,omitempty"`
}

// Action is a single proposal from the orchestrator agent to the daemon.
type Action struct {
	Type           ActionType   `json:"type"`
	TaskID         string       `json:"task_id,omitempty"`
	Reason         string       `json:"reason,omitempty"`
	Body           string       `json:"body,omitempty"`
	SectionName    string       `json:"section_name,omitempty"`
	SectionContent string       `json:"section_content,omitempty"`
	Subtasks       []SubtaskDef `json:"subtasks,omitempty"`
	Summary        string       `json:"summary,omitempty"`
	ArtifactPath   string       `json:"artifact_path,omitempty"`
}

// OrchestratorResponse is the full JSON response parsed from the orchestrator agent.
type OrchestratorResponse struct {
	Reasoning string   `json:"reasoning"`
	Actions   []Action `json:"actions"`
}

// ErrNotImplemented indicates an action whose schema is defined but execution is deferred.
var ErrNotImplemented = errors.New("action not implemented in this phase")

// RiskForAction returns the risk level for a given action type.
func RiskForAction(actionType ActionType) RiskLevel {
	switch actionType {
	case ActionDispatch, ActionSkip, ActionComment, ActionUpdateVoice, ActionRequestApproval:
		return RiskLow
	case ActionCloseWave, ActionCreateSubtasks, ActionPromoteKnowledge:
		return RiskMedium
	default:
		return RiskHigh
	}
}

// ValidateAction checks that an action is well-formed and references valid task IDs.
func ValidateAction(action Action, validTaskIDs []string) error {
	if !slices.Contains(allActionTypes, action.Type) {
		return fmt.Errorf("unknown action type: %q", action.Type)
	}

	switch action.Type {
	case ActionDispatch:
		if action.TaskID == "" {
			return fmt.Errorf("dispatch action requires task_id")
		}
		if !slices.Contains(validTaskIDs, action.TaskID) {
			return fmt.Errorf("dispatch action references unknown task_id: %q", action.TaskID)
		}

	case ActionSkip:
		if action.TaskID == "" {
			return fmt.Errorf("skip action requires task_id")
		}
		if action.Reason == "" {
			return fmt.Errorf("skip action requires reason")
		}

	case ActionComment:
		if action.TaskID == "" {
			return fmt.Errorf("comment action requires task_id")
		}
		if action.Body == "" {
			return fmt.Errorf("comment action requires body")
		}

	case ActionUpdateVoice:
		if action.SectionName == "" {
			return fmt.Errorf("update_voice action requires section_name")
		}
		if action.SectionContent == "" {
			return fmt.Errorf("update_voice action requires section_content")
		}

	case ActionRequestApproval:
		if action.TaskID == "" {
			return fmt.Errorf("request_approval action requires task_id")
		}

	case ActionCloseWave:
		// No extra validation.

	case ActionCreateSubtasks:
		if len(action.Subtasks) == 0 {
			return fmt.Errorf("create_subtasks action requires non-empty subtasks list")
		}

	case ActionPromoteKnowledge:
		if action.Summary == "" {
			return fmt.Errorf("promote_knowledge action requires summary")
		}
	}

	return nil
}

// IsIdempotent reports whether repeating the action produces the same outcome.
func IsIdempotent(actionType ActionType) bool {
	switch actionType {
	case ActionComment, ActionSkip, ActionRequestApproval, ActionUpdateVoice:
		return true
	default:
		return false
	}
}

// SchemaOnly reports whether the action is defined in Phase 3a but not yet executable.
func SchemaOnly(actionType ActionType) bool {
	switch actionType {
	case ActionCreateSubtasks, ActionPromoteKnowledge:
		return true
	default:
		return false
	}
}
