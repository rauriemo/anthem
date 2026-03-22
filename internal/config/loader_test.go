package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSplitFrontMatter(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantFront string
		wantBody  string
		wantErr   bool
	}{
		{
			name:      "valid front matter and body",
			input:     "---\nkey: value\n---\nbody content",
			wantFront: "key: value",
			wantBody:  "body content",
		},
		{
			name:      "multiline body",
			input:     "---\nk: v\n---\nline1\nline2\nline3",
			wantFront: "k: v",
			wantBody:  "line1\nline2\nline3",
		},
		{
			name:      "whitespace around content",
			input:     "  ---\n  key: value  \n---\n  body  ",
			wantFront: "key: value",
			wantBody:  "body",
		},
		{
			name:    "missing opening delimiter",
			input:   "key: value\n---\nbody",
			wantErr: true,
		},
		{
			name:    "missing closing delimiter",
			input:   "---\nkey: value\nbody",
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			front, body, err := splitFrontMatter([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := string(front); got != tt.wantFront {
				t.Errorf("front = %q, want %q", got, tt.wantFront)
			}
			if got := string(body); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		check   func(*testing.T, *Config, string)
		wantErr bool
	}{
		{
			name: "full config",
			input: `---
tracker:
  kind: github
  repo: "user/repo"
  labels:
    active: ["todo"]
    terminal: ["done"]
polling:
  interval_ms: 5000
workspace:
  root: "./ws"
agent:
  command: "claude"
  max_turns: 10
  max_concurrent: 5
  max_concurrent_per_label:
    planning: 1
system:
  workflow_changes_require_approval: false
  constraints:
    - "Run tests before merging"
server:
  port: 9090
---
Task: {{.Title}}`,
			check: func(t *testing.T, cfg *Config, body string) {
				if cfg.Tracker.Kind != "github" {
					t.Errorf("tracker.kind = %q, want github", cfg.Tracker.Kind)
				}
				if cfg.Tracker.Repo != "user/repo" {
					t.Errorf("tracker.repo = %q", cfg.Tracker.Repo)
				}
				if len(cfg.Tracker.Labels.Active) != 1 || cfg.Tracker.Labels.Active[0] != "todo" {
					t.Errorf("tracker.labels.active = %v", cfg.Tracker.Labels.Active)
				}
				if cfg.Polling.IntervalMS != 5000 {
					t.Errorf("polling.interval_ms = %d, want 5000", cfg.Polling.IntervalMS)
				}
				if cfg.Agent.MaxConcurrent != 5 {
					t.Errorf("agent.max_concurrent = %d, want 5", cfg.Agent.MaxConcurrent)
				}
				if cfg.Agent.MaxConcurrentPerLabel["planning"] != 1 {
					t.Errorf("max_concurrent_per_label[planning] = %d", cfg.Agent.MaxConcurrentPerLabel["planning"])
				}
				if cfg.System.WorkflowChangesRequireApproval {
					t.Error("system.workflow_changes_require_approval should be false")
				}
				if len(cfg.System.Constraints) != 1 || cfg.System.Constraints[0] != "Run tests before merging" {
					t.Errorf("system.constraints = %v", cfg.System.Constraints)
				}
				if cfg.Server.Port != 9090 {
					t.Errorf("server.port = %d, want 9090", cfg.Server.Port)
				}
				if body != "Task: {{.Title}}" {
					t.Errorf("body = %q", body)
				}
			},
		},
		{
			name: "defaults applied for missing fields",
			input: `---
tracker:
  kind: github
  repo: "u/r"
---
body`,
			check: func(t *testing.T, cfg *Config, _ string) {
				if cfg.Polling.IntervalMS != 10000 {
					t.Errorf("default polling.interval_ms = %d, want 10000", cfg.Polling.IntervalMS)
				}
				if cfg.Agent.Command != "claude" {
					t.Errorf("default agent.command = %q, want claude", cfg.Agent.Command)
				}
				if cfg.Agent.MaxConcurrent != 3 {
					t.Errorf("default agent.max_concurrent = %d, want 3", cfg.Agent.MaxConcurrent)
				}
				if !cfg.System.WorkflowChangesRequireApproval {
					t.Error("default system.workflow_changes_require_approval should be true")
				}
			},
		},
		{
			name: "rules parsed",
			input: `---
tracker:
  kind: github
  repo: "u/r"
rules:
  - match:
      labels: ["planning"]
    action: require_approval
    approval_label: "approved"
  - match:
      labels: ["bug"]
    action: auto_assign
---
body`,
			check: func(t *testing.T, cfg *Config, _ string) {
				if len(cfg.Rules) != 2 {
					t.Fatalf("rules count = %d, want 2", len(cfg.Rules))
				}
				r := cfg.Rules[0]
				if r.Action != "require_approval" {
					t.Errorf("rules[0].action = %q", r.Action)
				}
				if r.ApprovalLabel != "approved" {
					t.Errorf("rules[0].approval_label = %q", r.ApprovalLabel)
				}
				if len(r.Match.Labels) != 1 || r.Match.Labels[0] != "planning" {
					t.Errorf("rules[0].match.labels = %v", r.Match.Labels)
				}
			},
		},
		{
			name:    "invalid yaml",
			input:   "---\n: :\n---\nbody",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, body, err := Parse([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, cfg, body)
			}
		})
	}
}

func TestExpandEnvVars(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		envs   map[string]string
		want   string
	}{
		{
			name:  "single var",
			input: "repo: $MY_REPO",
			envs:  map[string]string{"MY_REPO": "user/repo"},
			want:  "repo: user/repo",
		},
		{
			name:  "multiple vars",
			input: "token: $TOKEN\nrepo: $REPO",
			envs:  map[string]string{"TOKEN": "abc123", "REPO": "x/y"},
			want:  "token: abc123\nrepo: x/y",
		},
		{
			name:  "unset var left as-is",
			input: "repo: $UNSET_VAR",
			envs:  map[string]string{},
			want:  "repo: $UNSET_VAR",
		},
		{
			name:  "no vars",
			input: "repo: literal",
			envs:  map[string]string{},
			want:  "repo: literal",
		},
		{
			name:  "var with underscore and numbers",
			input: "val: $MY_VAR_2",
			envs:  map[string]string{"MY_VAR_2": "ok"},
			want:  "val: ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envs {
				t.Setenv(k, v)
			}
			got := string(expandEnvVars([]byte(tt.input)))
			if got != tt.want {
				t.Errorf("expandEnvVars() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseWithEnvVars(t *testing.T) {
	t.Setenv("ANTHEM_TEST_REPO", "envuser/envrepo")

	input := `---
tracker:
  kind: github
  repo: $ANTHEM_TEST_REPO
---
body`

	cfg, _, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Tracker.Repo != "envuser/envrepo" {
		t.Errorf("repo = %q, want envuser/envrepo", cfg.Tracker.Repo)
	}
}

func TestRenderBody(t *testing.T) {
	type issue struct {
		Title      string
		Body       string
		Identifier string
		RepoURL    string
	}

	tests := []struct {
		name    string
		tmpl    string
		data    any
		want    string
		wantErr bool
	}{
		{
			name: "simple interpolation",
			tmpl: "Working on {{.Title}}",
			data: issue{Title: "Fix bug #42"},
			want: "Working on Fix bug #42",
		},
		{
			name: "multiple fields",
			tmpl: "Task: {{.Title}}\nBranch: anthem/{{.Identifier}}",
			data: issue{Title: "Add feature", Identifier: "GH-7"},
			want: "Task: Add feature\nBranch: anthem/GH-7",
		},
		{
			name: "sprig lower function",
			tmpl: "branch: {{.Title | lower}}",
			data: issue{Title: "FIX-BUG"},
			want: "branch: fix-bug",
		},
		{
			name: "sprig upper function",
			tmpl: "status: {{.Title | upper}}",
			data: issue{Title: "done"},
			want: "status: DONE",
		},
		{
			name: "sprig default function",
			tmpl: `model: {{.Title | default "unset"}}`,
			data: issue{Title: ""},
			want: "model: unset",
		},
		{
			name: "sprig replace function",
			tmpl: `slug: {{.Title | replace " " "-"}}`,
			data: issue{Title: "my cool feature"},
			want: "slug: my-cool-feature",
		},
		{
			name:    "invalid template syntax",
			tmpl:    "{{.Broken",
			data:    issue{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderBody(tt.tmpl, tt.data)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("RenderBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadFile(t *testing.T) {
	// Use the testdata fixture
	path := filepath.Join("..", "..", "testdata", "workflow.md")
	cfg, body, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error: %v", err)
	}
	if cfg.Tracker.Kind != "github" {
		t.Errorf("tracker.kind = %q", cfg.Tracker.Kind)
	}
	if cfg.Tracker.Repo != "test/repo" {
		t.Errorf("tracker.repo = %q", cfg.Tracker.Repo)
	}
	if cfg.Agent.MaxTurns != 3 {
		t.Errorf("agent.max_turns = %d", cfg.Agent.MaxTurns)
	}
	if body == "" {
		t.Error("body should not be empty")
	}
}

func TestLoadFile_NotFound(t *testing.T) {
	_, _, err := LoadFile("/nonexistent/workflow.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadFile_TempFile(t *testing.T) {
	content := `---
tracker:
  kind: local_json
workspace:
  root: "./ws"
agent:
  command: "claude"
---
hello`

	tmp := filepath.Join(t.TempDir(), "workflow.md")
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, body, err := LoadFile(tmp)
	if err != nil {
		t.Fatalf("LoadFile() error: %v", err)
	}
	if cfg.Tracker.Kind != "local_json" {
		t.Errorf("tracker.kind = %q", cfg.Tracker.Kind)
	}
	if body != "hello" {
		t.Errorf("body = %q", body)
	}
}
