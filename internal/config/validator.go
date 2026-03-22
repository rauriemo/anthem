package config

import (
	"errors"
	"fmt"
)

var validTrackerKinds = map[string]bool{
	"github":     true,
	"linear":     true,
	"local_json": true,
}

var validRuleActions = map[string]bool{
	"require_approval": true,
	"auto_assign":      true,
	"require_plan":     true,
	"max_cost":         true,
}

// Validate checks that a Config has all required fields and valid values.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	var errs []error

	if cfg.Tracker.Kind == "" {
		errs = append(errs, fmt.Errorf("tracker.kind is required"))
	} else if !validTrackerKinds[cfg.Tracker.Kind] {
		errs = append(errs, fmt.Errorf("tracker.kind %q is not valid (github, linear, local_json)", cfg.Tracker.Kind))
	}

	if cfg.Tracker.Kind == "github" && cfg.Tracker.Repo == "" {
		errs = append(errs, fmt.Errorf("tracker.repo is required for github tracker"))
	}

	if cfg.Agent.Command == "" {
		errs = append(errs, fmt.Errorf("agent.command is required"))
	}
	if cfg.Workspace.Root == "" {
		errs = append(errs, fmt.Errorf("workspace.root is required"))
	}
	if cfg.Agent.MaxConcurrent < 1 {
		errs = append(errs, fmt.Errorf("agent.max_concurrent must be >= 1"))
	}
	if cfg.Polling.IntervalMS < 1000 {
		errs = append(errs, fmt.Errorf("polling.interval_ms must be >= 1000"))
	}

	for i, r := range cfg.Rules {
		if r.Action == "" {
			errs = append(errs, fmt.Errorf("rules[%d].action is required", i))
		} else if !validRuleActions[r.Action] {
			errs = append(errs, fmt.Errorf("rules[%d].action %q is not valid", i, r.Action))
		}
		if r.Action == "require_approval" && r.ApprovalLabel == "" {
			errs = append(errs, fmt.Errorf("rules[%d].approval_label is required for require_approval action", i))
		}
		if r.Action == "max_cost" && r.MaxCost <= 0 {
			errs = append(errs, fmt.Errorf("rules[%d].max_cost must be > 0", i))
		}
	}

	return errors.Join(errs...)
}
