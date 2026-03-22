package rules

import (
	"testing"

	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/types"
)

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name        string
		rules       []config.RuleConfig
		task        types.Task
		wantCount   int
		wantActions []Action
	}{
		{
			name:      "no rules",
			rules:     nil,
			task:      types.Task{Labels: []string{"bug"}},
			wantCount: 0,
		},
		{
			name: "single label match",
			rules: []config.RuleConfig{
				{Match: config.RuleMatch{Labels: []string{"planning"}}, Action: "require_approval", ApprovalLabel: "approved"},
			},
			task:        types.Task{Labels: []string{"planning"}},
			wantCount:   1,
			wantActions: []Action{ActionRequireApproval},
		},
		{
			name: "label does not match",
			rules: []config.RuleConfig{
				{Match: config.RuleMatch{Labels: []string{"planning"}}, Action: "require_approval", ApprovalLabel: "approved"},
			},
			task:      types.Task{Labels: []string{"bug"}},
			wantCount: 0,
		},
		{
			name: "multi-label rule requires all labels present",
			rules: []config.RuleConfig{
				{Match: config.RuleMatch{Labels: []string{"planning", "high-priority"}}, Action: "require_approval", ApprovalLabel: "approved"},
			},
			task:      types.Task{Labels: []string{"planning"}},
			wantCount: 0,
		},
		{
			name: "multi-label rule matches when all present",
			rules: []config.RuleConfig{
				{Match: config.RuleMatch{Labels: []string{"planning", "high-priority"}}, Action: "require_approval", ApprovalLabel: "approved"},
			},
			task:        types.Task{Labels: []string{"planning", "high-priority", "extra"}},
			wantCount:   1,
			wantActions: []Action{ActionRequireApproval},
		},
		{
			name: "multiple rules can match",
			rules: []config.RuleConfig{
				{Match: config.RuleMatch{Labels: []string{"bug"}}, Action: "auto_assign"},
				{Match: config.RuleMatch{Labels: []string{"bug"}}, Action: "max_cost", MaxCost: 1.0},
			},
			task:        types.Task{Labels: []string{"bug"}},
			wantCount:   2,
			wantActions: []Action{ActionAutoAssign, ActionMaxCost},
		},
		{
			name: "rule with empty labels matches everything",
			rules: []config.RuleConfig{
				{Match: config.RuleMatch{}, Action: "require_plan"},
			},
			task:        types.Task{Labels: []string{"anything"}},
			wantCount:   1,
			wantActions: []Action{ActionRequirePlan},
		},
		{
			name: "max_cost preserves value",
			rules: []config.RuleConfig{
				{Match: config.RuleMatch{Labels: []string{"expensive"}}, Action: "max_cost", MaxCost: 5.50},
			},
			task:      types.Task{Labels: []string{"expensive"}},
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewEngine(tt.rules)
			results := engine.Evaluate(tt.task)

			if len(results) != tt.wantCount {
				t.Fatalf("Evaluate() returned %d results, want %d", len(results), tt.wantCount)
			}

			for i, wantAction := range tt.wantActions {
				if results[i].Action != wantAction {
					t.Errorf("results[%d].Action = %q, want %q", i, results[i].Action, wantAction)
				}
				if !results[i].Matched {
					t.Errorf("results[%d].Matched = false, want true", i)
				}
			}

			// Check max_cost preservation
			if tt.name == "max_cost preserves value" && results[0].MaxCost != 5.50 {
				t.Errorf("MaxCost = %f, want 5.50", results[0].MaxCost)
			}
		})
	}
}

func TestNeedsApproval(t *testing.T) {
	tests := []struct {
		name          string
		labels        []string
		approvalLabel string
		want          bool
	}{
		{
			name:          "needs approval when label missing",
			labels:        []string{"planning", "todo"},
			approvalLabel: "approved",
			want:          true,
		},
		{
			name:          "does not need approval when label present",
			labels:        []string{"planning", "approved"},
			approvalLabel: "approved",
			want:          false,
		},
		{
			name:          "empty labels needs approval",
			labels:        nil,
			approvalLabel: "approved",
			want:          true,
		},
		{
			name:          "exact match required",
			labels:        []string{"approved-by-lead"},
			approvalLabel: "approved",
			want:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := types.Task{Labels: tt.labels}
			if got := NeedsApproval(task, tt.approvalLabel); got != tt.want {
				t.Errorf("NeedsApproval() = %v, want %v", got, tt.want)
			}
		})
	}
}
