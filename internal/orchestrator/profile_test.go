package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rauriemo/anthem/internal/agent"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/workspace"
)

func newProfileTestOrch(t *testing.T, cfg config.Config) *Orchestrator {
	t.Helper()
	return New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      tracker.NewMockTracker(nil),
		Runner:       agent.NewMockRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})
}

func TestFocusToProfile_Mapping(t *testing.T) {
	tests := []struct {
		focus string
		want  string
	}{
		{"security", "security-explorer"},
		{"tests", "test-explorer"},
		{"architecture", "explorer"},
		{"dependencies", "explorer"},
		{"patterns", "explorer"},
		{"", "explorer"},
		{"unknown", "explorer"},
	}
	for _, tt := range tests {
		t.Run(tt.focus, func(t *testing.T) {
			got := focusToProfile(tt.focus)
			if got != tt.want {
				t.Errorf("focusToProfile(%q) = %q, want %q", tt.focus, got, tt.want)
			}
		})
	}
}

func TestResolveProfile_BaseConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.MaxTurns = 7
	cfg.Agent.Model = "sonnet"
	cfg.Agent.DeniedTools = []string{"Bash"}

	orch := newProfileTestOrch(t, cfg)

	opts, err := orch.resolveProfile("nonexistent-profile", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if opts.MaxTurns != 7 {
		t.Errorf("MaxTurns = %d, want 7", opts.MaxTurns)
	}
	if opts.Model != "sonnet" {
		t.Errorf("Model = %q, want %q", opts.Model, "sonnet")
	}
	if len(opts.DeniedTools) != 1 || opts.DeniedTools[0] != "Bash" {
		t.Errorf("DeniedTools = %v, want [Bash]", opts.DeniedTools)
	}
}

func TestResolveProfile_ToolMerge(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.DeniedTools = []string{"Bash"}
	cfg.Agent.Profiles["custom"] = config.AgentProfile{
		DeniedTools:  []string{"Write"},
		AllowedTools: []string{"Read", "Grep"},
	}

	orch := newProfileTestOrch(t, cfg)

	opts, err := orch.resolveProfile("custom", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// DeniedTools should be base + profile
	if len(opts.DeniedTools) != 2 {
		t.Fatalf("DeniedTools = %v, want [Bash Write]", opts.DeniedTools)
	}
	// AllowedTools should be replaced by profile
	if len(opts.AllowedTools) != 2 || opts.AllowedTools[0] != "Read" {
		t.Errorf("AllowedTools = %v, want [Read Grep]", opts.AllowedTools)
	}
}

func TestResolveProfile_ModelOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.Model = "sonnet"
	cfg.Agent.Profiles["fast"] = config.AgentProfile{Model: "haiku"}

	orch := newProfileTestOrch(t, cfg)

	opts, _ := orch.resolveProfile("fast", t.TempDir())
	if opts.Model != "haiku" {
		t.Errorf("Model = %q, want %q", opts.Model, "haiku")
	}

	opts2, _ := orch.resolveProfile("coder", t.TempDir())
	if opts2.Model != "sonnet" {
		t.Errorf("base model should be preserved: got %q, want %q", opts2.Model, "sonnet")
	}
}

func TestResolveProfile_TurnsOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.MaxTurns = 5
	cfg.Agent.Profiles["deep"] = config.AgentProfile{MaxTurns: 20}

	orch := newProfileTestOrch(t, cfg)

	opts, _ := orch.resolveProfile("deep", t.TempDir())
	if opts.MaxTurns != 20 {
		t.Errorf("MaxTurns = %d, want 20", opts.MaxTurns)
	}
}

func TestResolveProfile_MCPRefsResolved(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.MCPServers = map[string]config.MCPServerConfig{
		"semgrep": {Command: "semgrep-mcp", Args: []string{"--config", "auto"}},
		"unity":   {Command: "npx", Args: []string{"-y", "unity-mcp"}},
	}
	cfg.Agent.Profiles["sec"] = config.AgentProfile{
		MCPRefs: []string{"semgrep"},
	}

	orch := newProfileTestOrch(t, cfg)
	wsDir := t.TempDir()

	if _, err := orch.resolveProfile("sec", wsDir); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(wsDir, ".mcp.json"))
	if err != nil {
		t.Fatal("expected .mcp.json to be written")
	}
	var mcp struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &mcp); err != nil {
		t.Fatal(err)
	}
	if _, ok := mcp.MCPServers["semgrep"]; !ok {
		t.Error("expected semgrep in .mcp.json")
	}
	if _, ok := mcp.MCPServers["unity"]; ok {
		t.Error("unity should NOT be in .mcp.json (not referenced by profile)")
	}
}

func TestResolveProfile_SkillRefsResolved(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.Profiles["skilled"] = config.AgentProfile{
		SkillRefs: []string{"./skills/local-one"},
	}

	orch := newProfileTestOrch(t, cfg)
	wsDir := t.TempDir()

	if _, err := orch.resolveProfile("skilled", wsDir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(wsDir, ".claude", "skills")); !os.IsNotExist(err) {
		t.Error("project-local skills should not trigger .claude/skills/ creation")
	}
}

func TestResolveProfile_GlobalBaseline(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.Skills = []string{"anthem://pr-workflow"}
	cfg.Agent.Profiles["custom"] = config.AgentProfile{
		SkillRefs: []string{"anthem://owasp"},
	}

	orch := newProfileTestOrch(t, cfg)

	// Global + profile skills should be merged
	// (We can't easily test the PrepareSkills call without a builtin dir,
	// but we verify it doesn't error)
	_, err := orch.resolveProfile("custom", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
}

func TestResolveProfile_ExplorerProfiles(t *testing.T) {
	cfg := config.DefaultConfig()
	orch := newProfileTestOrch(t, cfg)

	tests := []struct {
		profile string
		wantLen int // expected DeniedTools includes profile's Write/Edit/MultiEdit
	}{
		{"security-explorer", 3},
		{"test-explorer", 3},
		{"explorer", 3},
	}

	for _, tt := range tests {
		t.Run(tt.profile, func(t *testing.T) {
			opts, _ := orch.resolveProfile(tt.profile, t.TempDir())
			if len(opts.DeniedTools) < tt.wantLen {
				t.Errorf("DeniedTools for %s = %v, expected at least %d entries",
					tt.profile, opts.DeniedTools, tt.wantLen)
			}
		})
	}
}
