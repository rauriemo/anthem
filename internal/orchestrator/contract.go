package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
	ActionDispatch           ActionType = "dispatch"
	ActionSkip               ActionType = "skip"
	ActionComment            ActionType = "comment"
	ActionUpdateVoice        ActionType = "update_voice"
	ActionRequestApproval    ActionType = "request_approval"
	ActionCloseWave          ActionType = "close_wave"
	ActionCreateSubtasks     ActionType = "create_subtasks"
	ActionPromoteKnowledge   ActionType = "promote_knowledge"
	ActionReply              ActionType = "reply"
	ActionDisplay            ActionType = "display"
	ActionRequestMaintenance ActionType = "request_maintenance"
	ActionUpdateAgentMeta    ActionType = "update_agent_meta"
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
	ActionReply,
	ActionDisplay,
	ActionRequestMaintenance,
	ActionUpdateAgentMeta,
}

// SubtaskDef defines a subtask to be created by the create_subtasks action.
type SubtaskDef struct {
	Title     string          `json:"title"`
	Body      string          `json:"body"`
	Labels    []string        `json:"labels,omitempty"`
	DependsOn FlexStringSlice `json:"depends_on,omitempty"`
}

// FlexStringSlice unmarshals a JSON array of mixed strings and numbers into []string.
// This handles LLM output where depends_on may be [1, 2] or ["1", "2"].
type FlexStringSlice []string

func (f *FlexStringSlice) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			result = append(result, s)
			continue
		}
		var n float64
		if err := json.Unmarshal(item, &n); err == nil {
			result = append(result, strconv.Itoa(int(n)))
			continue
		}
		result = append(result, string(item))
	}
	*f = result
	return nil
}

// Action is a single proposal from the orchestrator agent to the daemon.
type Action struct {
	Type              ActionType   `json:"type"`
	TaskID            string       `json:"task_id,omitempty"`
	Profile           string       `json:"profile,omitempty"`
	Reason            string       `json:"reason,omitempty"`
	Body              string       `json:"body,omitempty"`
	SectionName       string       `json:"section_name,omitempty"`
	SectionContent    string       `json:"section_content,omitempty"`
	Subtasks          []SubtaskDef `json:"subtasks,omitempty"`
	Summary           string       `json:"summary,omitempty"`
	ArtifactPath      string       `json:"artifact_path,omitempty"`
	MaintenanceType   string       `json:"maintenance_type,omitempty"`
	AutoApprovable    bool         `json:"auto_approvable,omitempty"`
	DisplayKind       string       `json:"display_kind,omitempty"`
	DisplayContent    string       `json:"display_content,omitempty"`
	DisplayTitle      string       `json:"display_title,omitempty"`
	DisplayLanguage   string       `json:"display_language,omitempty"`
	DisplayData       any          `json:"display_data,omitempty"`
	AgentFile         string       `json:"agent_file,omitempty"`
	AgentName         string       `json:"agent_name,omitempty"`
	AgentDescription  string       `json:"agent_description,omitempty"`
	AgentRole         string       `json:"agent_role,omitempty"`
	AgentCapabilities []string     `json:"agent_capabilities,omitempty"`
	AgentIcon         string       `json:"agent_icon,omitempty"`
	AgentQuotes       []string     `json:"agent_quotes,omitempty"`
}

// OrchestratorResponse is the full JSON response parsed from the orchestrator agent.
type OrchestratorResponse struct {
	Reasoning     string   `json:"reasoning"`
	Actions       []Action `json:"actions"`
	ContextUpdate string   `json:"context_update,omitempty"`
}

// ErrNotImplemented indicates an action whose schema is defined but execution is deferred.
var ErrNotImplemented = errors.New("action not implemented in this phase")

// RiskForAction returns the risk level for a given action type.
func RiskForAction(actionType ActionType) RiskLevel {
	switch actionType {
	case ActionDispatch, ActionSkip, ActionComment, ActionUpdateVoice, ActionRequestApproval, ActionReply, ActionDisplay, ActionUpdateAgentMeta:
		return RiskLow
	case ActionCloseWave, ActionCreateSubtasks, ActionPromoteKnowledge, ActionRequestMaintenance:
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
		if action.AgentFile != "" {
			if err := validateAgentFile(action.AgentFile); err != nil {
				return fmt.Errorf("update_voice agent_file: %w", err)
			}
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

	case ActionReply:
		if action.Body == "" {
			return fmt.Errorf("reply action requires body")
		}

	case ActionDisplay:
		if action.DisplayKind == "" {
			return fmt.Errorf("display action requires display_kind")
		}

	case ActionRequestMaintenance:
		if action.MaintenanceType == "" {
			return fmt.Errorf("request_maintenance action requires maintenance_type")
		}
		if action.Reason == "" {
			return fmt.Errorf("request_maintenance action requires reason")
		}

	case ActionUpdateAgentMeta:
		if action.AgentFile == "" {
			return fmt.Errorf("update_agent_meta action requires agent_file")
		}
		if err := validateAgentFile(action.AgentFile); err != nil {
			return fmt.Errorf("update_agent_meta agent_file: %w", err)
		}
		if action.AgentName == "" {
			return fmt.Errorf("update_agent_meta action requires agent_name")
		}
	}

	return nil
}

// validateAgentFile checks that an agent_file value is a safe basename within agents/.
func validateAgentFile(name string) error {
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("must be a basename with no path separators, got %q", name)
	}
	if !strings.HasSuffix(name, ".md") {
		return fmt.Errorf("must end in .md, got %q", name)
	}
	if cleaned := filepath.Base(name); cleaned != name {
		return fmt.Errorf("must be a simple filename, got %q", name)
	}
	return nil
}

// IsIdempotent reports whether repeating the action produces the same outcome.
func IsIdempotent(actionType ActionType) bool {
	switch actionType {
	case ActionComment, ActionSkip, ActionRequestApproval, ActionUpdateVoice, ActionReply, ActionDisplay, ActionUpdateAgentMeta:
		return true
	default:
		return false
	}
}

// SchemaOnly reports whether the action's schema is defined but execution is deferred to a future phase.
func SchemaOnly(_ ActionType) bool {
	return false
}

// IsLoopOnlyAction reports whether an action is only valid in Loop mode.
//
// Loop-only actions touch the issue tracker (create issues, update
// status, add comments, add labels, dispatch queued tasks) or
// manipulate wave coordination state. These are all concepts that
// only exist in the issue-driven Loop mode -- the Chat / Plan /
// Execute paths are light "claude code CLI + context injection"
// prompts and must never create or mutate remote work items on the
// user's behalf.
//
// This predicate powers Layer 1 of a three-layer route-isolation
// boundary. All three layers must hold for the invariant "no
// remote-tracker mutation outside Loop mode" to be enforceable:
//
//   - Layer 1 (this file + executeActions): blocks the
//     orchestrator-action dispatch path. The orchestrator gates
//     executeActions on IsLoopOnlyAction -- when CurrentMode !=
//     ModeLoop, loop-only actions are logged, audited as blocked,
//     and dropped without calling the tracker. This is the
//     immediate fix for the "9 issues silently created during an
//     Execute-mode approval gate" regression.
//   - Layer 2 (runner_gate.go, modeGatedRunner): blocks the
//     Claude-tool path. Appends DefaultInteractiveDeniedTools
//     (Bash(gh *), WebFetch, mcp__github__*, etc.) to RunOpts and
//     ContinueOpts so Claude cannot shell out to gh/curl or fetch
//     GitHub APIs directly even under
//     --dangerously-skip-permissions.
//   - Layer 3 (tracker_gate.go, modeGatedTracker): blocks the
//     direct Go-code path. Every mutating method on
//     tracker.IssueTracker returns
//     ErrTrackerMutationDeniedInMode outside Loop, so any future
//     refactor that reaches for o.tracker.CreateIssue outside
//     executeActions is also caught.
//
// Keep this set conservative: adding a new action here silently
// disables it in Chat/Plan/Execute, so only include actions whose
// intent is meaningless outside of Loop.
func IsLoopOnlyAction(actionType ActionType) bool {
	switch actionType {
	case ActionDispatch,
		ActionSkip,
		ActionComment,
		ActionRequestApproval,
		ActionCreateSubtasks,
		ActionCloseWave:
		return true
	default:
		return false
	}
}
