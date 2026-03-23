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
			engine := NewEngine(tt.rules, nil)
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

func TestTitlePatternMatching(t *testing.T) {
	tests := []struct {
		name      string
		rules     []config.RuleConfig
		task      types.Task
		wantCount int
	}{
		{
			name: "title pattern matches",
			rules: []config.RuleConfig{
				{Match: config.RuleMatch{TitlePattern: `^fix:`}, Action: "auto_assign", AutoAssignee: "alice"},
			},
			task:      types.Task{Title: "fix: broken login"},
			wantCount: 1,
		},
		{
			name: "title pattern does not match",
			rules: []config.RuleConfig{
				{Match: config.RuleMatch{TitlePattern: `^fix:`}, Action: "auto_assign", AutoAssignee: "alice"},
			},
			task:      types.Task{Title: "feat: new button"},
			wantCount: 0,
		},
		{
			name: "invalid regex skips rule",
			rules: []config.RuleConfig{
				{Match: config.RuleMatch{TitlePattern: `[invalid`}, Action: "auto_assign", AutoAssignee: "alice"},
			},
			task:      types.Task{Title: "anything"},
			wantCount: 0,
		},
		{
			name: "title pattern combined with labels — both must match",
			rules: []config.RuleConfig{
				{Match: config.RuleMatch{Labels: []string{"bug"}, TitlePattern: `(?i)urgent`}, Action: "max_cost", MaxCost: 10.0},
			},
			task:      types.Task{Title: "URGENT: crash on startup", Labels: []string{"bug"}},
			wantCount: 1,
		},
		{
			name: "title pattern matches but labels dont",
			rules: []config.RuleConfig{
				{Match: config.RuleMatch{Labels: []string{"bug"}, TitlePattern: `(?i)urgent`}, Action: "max_cost", MaxCost: 10.0},
			},
			task:      types.Task{Title: "URGENT: crash on startup", Labels: []string{"feature"}},
			wantCount: 0,
		},
		{
			name: "labels match but title pattern doesnt",
			rules: []config.RuleConfig{
				{Match: config.RuleMatch{Labels: []string{"bug"}, TitlePattern: `(?i)urgent`}, Action: "max_cost", MaxCost: 10.0},
			},
			task:      types.Task{Title: "minor: typo fix", Labels: []string{"bug"}},
			wantCount: 0,
		},
		{
			name: "empty title pattern matches everything",
			rules: []config.RuleConfig{
				{Match: config.RuleMatch{TitlePattern: ""}, Action: "require_plan"},
			},
			task:      types.Task{Title: "anything"},
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewEngine(tt.rules, nil)
			results := engine.Evaluate(tt.task)
			if len(results) != tt.wantCount {
				t.Fatalf("Evaluate() returned %d results, want %d", len(results), tt.wantCount)
			}
		})
	}
}

func TestAutoAssigneeInResult(t *testing.T) {
	rules := []config.RuleConfig{
		{Match: config.RuleMatch{Labels: []string{"bug"}}, Action: "auto_assign", AutoAssignee: "bob"},
	}
	engine := NewEngine(rules, nil)
	results := engine.Evaluate(types.Task{Labels: []string{"bug"}})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].AutoAssignee != "bob" {
		t.Errorf("AutoAssignee = %q, want %q", results[0].AutoAssignee, "bob")
	}
	if results[0].Action != ActionAutoAssign {
		t.Errorf("Action = %q, want %q", results[0].Action, ActionAutoAssign)
	}
}

func TestRegexCacheReuse(t *testing.T) {
	rules := []config.RuleConfig{
		{Match: config.RuleMatch{TitlePattern: `^fix:`}, Action: "auto_assign"},
	}
	engine := NewEngine(rules, nil)

	// Evaluate twice to exercise cache
	engine.Evaluate(types.Task{Title: "fix: bug"})
	engine.Evaluate(types.Task{Title: "fix: another"})

	engine.mu.Lock()
	cacheLen := len(engine.regexCache)
	engine.mu.Unlock()

	if cacheLen != 1 {
		t.Errorf("regex cache has %d entries, want 1", cacheLen)
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
