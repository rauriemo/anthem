package config

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	validBase := func() Config {
		cfg := DefaultConfig()
		cfg.Tracker.Kind = "github"
		cfg.Tracker.Repo = "user/repo"
		return cfg
	}

	tests := []struct {
		name    string
		modify  func(*Config)
		wantErr string
	}{
		{
			name:   "valid config",
			modify: func(_ *Config) {},
		},
		{
			name:    "missing tracker kind",
			modify:  func(c *Config) { c.Tracker.Kind = "" },
			wantErr: "tracker.kind is required",
		},
		{
			name:    "invalid tracker kind",
			modify:  func(c *Config) { c.Tracker.Kind = "jira" },
			wantErr: "not valid",
		},
		{
			name:    "github tracker missing repo",
			modify:  func(c *Config) { c.Tracker.Repo = "" },
			wantErr: "tracker.repo is required for github",
		},
		{
			name:   "local_json tracker no repo needed",
			modify: func(c *Config) { c.Tracker.Kind = "local_json"; c.Tracker.Repo = "" },
		},
		{
			name:    "missing agent command",
			modify:  func(c *Config) { c.Agent.Command = "" },
			wantErr: "agent.command is required",
		},
		{
			name:    "missing workspace root",
			modify:  func(c *Config) { c.Workspace.Root = "" },
			wantErr: "workspace.root is required",
		},
		{
			name:    "max_concurrent < 1",
			modify:  func(c *Config) { c.Agent.MaxConcurrent = 0 },
			wantErr: "max_concurrent must be >= 1",
		},
		{
			name:    "interval_ms too low",
			modify:  func(c *Config) { c.Polling.IntervalMS = 500 },
			wantErr: "interval_ms must be >= 1000",
		},
		{
			name: "rule with empty action",
			modify: func(c *Config) {
				c.Rules = []RuleConfig{{Match: RuleMatch{Labels: []string{"bug"}}, Action: ""}}
			},
			wantErr: "rules[0].action is required",
		},
		{
			name: "rule with invalid action",
			modify: func(c *Config) {
				c.Rules = []RuleConfig{{Match: RuleMatch{Labels: []string{"bug"}}, Action: "explode"}}
			},
			wantErr: "not valid",
		},
		{
			name: "require_approval without approval_label",
			modify: func(c *Config) {
				c.Rules = []RuleConfig{{
					Match:  RuleMatch{Labels: []string{"bug"}},
					Action: "require_approval",
				}}
			},
			wantErr: "approval_label is required",
		},
		{
			name: "max_cost rule with zero cost",
			modify: func(c *Config) {
				c.Rules = []RuleConfig{{
					Match:  RuleMatch{Labels: []string{"expensive"}},
					Action: "max_cost",
				}}
			},
			wantErr: "max_cost must be > 0",
		},
		{
			name: "valid require_approval rule",
			modify: func(c *Config) {
				c.Rules = []RuleConfig{{
					Match:         RuleMatch{Labels: []string{"planning"}},
					Action:        "require_approval",
					ApprovalLabel: "approved",
				}}
			},
		},
		{
			name: "valid max_cost rule",
			modify: func(c *Config) {
				c.Rules = []RuleConfig{{
					Match:   RuleMatch{Labels: []string{"expensive"}},
					Action:  "max_cost",
					MaxCost: 5.0,
				}}
			},
		},
		{
			name: "multiple errors reported",
			modify: func(c *Config) {
				c.Tracker.Kind = ""
				c.Agent.Command = ""
				c.Workspace.Root = ""
			},
			wantErr: "tracker.kind",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validBase()
			tt.modify(&cfg)
			err := Validate(&cfg)

			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Tracker.Kind = ""
	cfg.Agent.Command = ""
	cfg.Workspace.Root = ""
	cfg.Polling.IntervalMS = 0

	err := Validate(&cfg)
	if err == nil {
		t.Fatal("expected errors")
	}
	msg := err.Error()
	// Should report all errors, not just the first
	if !strings.Contains(msg, "tracker.kind") {
		t.Error("missing tracker.kind error")
	}
	if !strings.Contains(msg, "agent.command") {
		t.Error("missing agent.command error")
	}
	if !strings.Contains(msg, "workspace.root") {
		t.Error("missing workspace.root error")
	}
	if !strings.Contains(msg, "interval_ms") {
		t.Error("missing interval_ms error")
	}
}
