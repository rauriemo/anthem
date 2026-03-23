package orchestrator

import (
	"testing"
)

func TestValidateAction(t *testing.T) {
	validIDs := []string{"task-1", "task-2", "task-3"}

	tests := []struct {
		name    string
		action  Action
		wantErr bool
		errMsg  string
	}{
		// dispatch
		{
			name:   "dispatch valid",
			action: Action{Type: ActionDispatch, TaskID: "task-1"},
		},
		{
			name:    "dispatch missing task_id",
			action:  Action{Type: ActionDispatch},
			wantErr: true,
			errMsg:  "dispatch action requires task_id",
		},
		{
			name:    "dispatch unknown task_id",
			action:  Action{Type: ActionDispatch, TaskID: "task-99"},
			wantErr: true,
			errMsg:  "unknown task_id",
		},

		// skip
		{
			name:   "skip valid",
			action: Action{Type: ActionSkip, TaskID: "task-1", Reason: "blocked"},
		},
		{
			name:    "skip missing task_id",
			action:  Action{Type: ActionSkip, Reason: "blocked"},
			wantErr: true,
			errMsg:  "skip action requires task_id",
		},
		{
			name:    "skip missing reason",
			action:  Action{Type: ActionSkip, TaskID: "task-1"},
			wantErr: true,
			errMsg:  "skip action requires reason",
		},

		// comment
		{
			name:   "comment valid",
			action: Action{Type: ActionComment, TaskID: "task-2", Body: "looks good"},
		},
		{
			name:    "comment missing task_id",
			action:  Action{Type: ActionComment, Body: "looks good"},
			wantErr: true,
			errMsg:  "comment action requires task_id",
		},
		{
			name:    "comment missing body",
			action:  Action{Type: ActionComment, TaskID: "task-2"},
			wantErr: true,
			errMsg:  "comment action requires body",
		},

		// update_voice
		{
			name:   "update_voice valid",
			action: Action{Type: ActionUpdateVoice, SectionName: "tone", SectionContent: "casual"},
		},
		{
			name:    "update_voice missing section_name",
			action:  Action{Type: ActionUpdateVoice, SectionContent: "casual"},
			wantErr: true,
			errMsg:  "update_voice action requires section_name",
		},
		{
			name:    "update_voice missing section_content",
			action:  Action{Type: ActionUpdateVoice, SectionName: "tone"},
			wantErr: true,
			errMsg:  "update_voice action requires section_content",
		},

		// request_approval
		{
			name:   "request_approval valid",
			action: Action{Type: ActionRequestApproval, TaskID: "task-3"},
		},
		{
			name:    "request_approval missing task_id",
			action:  Action{Type: ActionRequestApproval},
			wantErr: true,
			errMsg:  "request_approval action requires task_id",
		},

		// close_wave
		{
			name:   "close_wave valid",
			action: Action{Type: ActionCloseWave},
		},

		// create_subtasks
		{
			name: "create_subtasks valid",
			action: Action{Type: ActionCreateSubtasks, Subtasks: []SubtaskDef{
				{Title: "sub-1", Body: "do thing"},
			}},
		},
		{
			name:    "create_subtasks empty list",
			action:  Action{Type: ActionCreateSubtasks},
			wantErr: true,
			errMsg:  "create_subtasks action requires non-empty subtasks list",
		},

		// promote_knowledge
		{
			name:   "promote_knowledge valid",
			action: Action{Type: ActionPromoteKnowledge, Summary: "auth flow documented"},
		},
		{
			name:    "promote_knowledge missing summary",
			action:  Action{Type: ActionPromoteKnowledge},
			wantErr: true,
			errMsg:  "promote_knowledge action requires summary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAction(tt.action, validIDs)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errMsg)
				}
				if got := err.Error(); !contains(got, tt.errMsg) {
					t.Fatalf("error %q does not contain %q", got, tt.errMsg)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateAction_UnknownType(t *testing.T) {
	err := ValidateAction(Action{Type: "explode"}, nil)
	if err == nil {
		t.Fatal("expected error for unknown action type")
	}
	if got := err.Error(); !contains(got, "unknown action type") {
		t.Fatalf("error %q does not mention unknown action type", got)
	}
}

func TestValidateAction_InvalidTaskID(t *testing.T) {
	err := ValidateAction(Action{Type: ActionDispatch, TaskID: "nope"}, []string{"task-1"})
	if err == nil {
		t.Fatal("expected error for invalid task ID")
	}
	if got := err.Error(); !contains(got, "unknown task_id") {
		t.Fatalf("error %q does not mention unknown task_id", got)
	}
}

func TestRiskForAction(t *testing.T) {
	tests := []struct {
		action ActionType
		want   RiskLevel
	}{
		{ActionDispatch, RiskLow},
		{ActionSkip, RiskLow},
		{ActionComment, RiskLow},
		{ActionUpdateVoice, RiskLow},
		{ActionRequestApproval, RiskLow},
		{ActionCloseWave, RiskMedium},
		{ActionCreateSubtasks, RiskMedium},
		{ActionPromoteKnowledge, RiskMedium},
		{"unknown", RiskHigh},
	}

	for _, tt := range tests {
		t.Run(string(tt.action), func(t *testing.T) {
			if got := RiskForAction(tt.action); got != tt.want {
				t.Fatalf("RiskForAction(%q) = %q, want %q", tt.action, got, tt.want)
			}
		})
	}
}

func TestIsIdempotent(t *testing.T) {
	tests := []struct {
		action ActionType
		want   bool
	}{
		{ActionComment, true},
		{ActionSkip, true},
		{ActionRequestApproval, true},
		{ActionUpdateVoice, true},
		{ActionDispatch, false},
		{ActionCloseWave, false},
		{ActionCreateSubtasks, false},
		{ActionPromoteKnowledge, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.action), func(t *testing.T) {
			if got := IsIdempotent(tt.action); got != tt.want {
				t.Fatalf("IsIdempotent(%q) = %v, want %v", tt.action, got, tt.want)
			}
		})
	}
}

func TestSchemaOnly(t *testing.T) {
	tests := []struct {
		action ActionType
		want   bool
	}{
		{ActionCreateSubtasks, true},
		{ActionPromoteKnowledge, true},
		{ActionDispatch, false},
		{ActionSkip, false},
		{ActionComment, false},
		{ActionUpdateVoice, false},
		{ActionRequestApproval, false},
		{ActionCloseWave, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.action), func(t *testing.T) {
			if got := SchemaOnly(tt.action); got != tt.want {
				t.Fatalf("SchemaOnly(%q) = %v, want %v", tt.action, got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
