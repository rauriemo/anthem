package rules

import (
	"log/slog"
	"regexp"
	"sync"

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
	AutoAssignee  string
	Matched       bool
}

// Engine evaluates rules against tasks.
type Engine struct {
	rules  []config.RuleConfig
	logger *slog.Logger

	mu         sync.Mutex
	regexCache map[string]*regexp.Regexp
}

func NewEngine(rules []config.RuleConfig, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{
		rules:      rules,
		logger:     logger,
		regexCache: make(map[string]*regexp.Regexp),
	}
}

// Evaluate returns all matching rule results for a task.
func (e *Engine) Evaluate(task types.Task) []EvalResult {
	var results []EvalResult
	for _, r := range e.rules {
		if e.matchesRule(r, task) {
			results = append(results, EvalResult{
				Action:        Action(r.Action),
				ApprovalLabel: r.ApprovalLabel,
				MaxCost:       r.MaxCost,
				AutoAssignee:  r.AutoAssignee,
				Matched:       true,
			})
		}
	}
	return results
}

func (e *Engine) matchesRule(rule config.RuleConfig, task types.Task) bool {
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

	if rule.Match.TitlePattern != "" {
		re, ok := e.getOrCompileRegex(rule.Match.TitlePattern)
		if !ok {
			return false
		}
		if !re.MatchString(task.Title) {
			return false
		}
	}

	return true
}

func (e *Engine) getOrCompileRegex(pattern string) (*regexp.Regexp, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if re, ok := e.regexCache[pattern]; ok {
		return re, re != nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		e.logger.Warn("invalid title_pattern in rule, skipping", "pattern", pattern, "error", err)
		e.regexCache[pattern] = nil
		return nil, false
	}
	e.regexCache[pattern] = re
	return re, true
}
