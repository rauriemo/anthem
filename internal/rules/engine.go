package rules

import (
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/types"
)

type Action string

const (
	ActionRequireApproval Action = "require_approval"
	ActionAutoAssign      Action = "auto_assign"
	ActionRequirePlan     Action = "require_plan"
	ActionMaxCost         Action = "max_cost"
)

type EvalResult struct {
	Action        Action
	ApprovalLabel string
	MaxCost       float64
	Matched       bool
}

// Engine evaluates rules against tasks.
type Engine struct {
	rules []config.RuleConfig
}

func NewEngine(rules []config.RuleConfig) *Engine {
	return &Engine{rules: rules}
}

// Evaluate returns all matching rule results for a task.
func (e *Engine) Evaluate(task types.Task) []EvalResult {
	var results []EvalResult
	for _, r := range e.rules {
		if matchesRule(r, task) {
			results = append(results, EvalResult{
				Action:        Action(r.Action),
				ApprovalLabel: r.ApprovalLabel,
				MaxCost:       r.MaxCost,
				Matched:       true,
			})
		}
	}
	return results
}

func matchesRule(rule config.RuleConfig, task types.Task) bool {
	if len(rule.Match.Labels) > 0 {
		taskLabels := make(map[string]bool)
		for _, l := range task.Labels {
			taskLabels[l] = true
		}
		for _, required := range rule.Match.Labels {
			if !taskLabels[required] {
				return false
			}
		}
	}
	return true
}
