package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rauriemo/anthem/internal/config"
)

func TestWriteMCPConfig_SingleServer(t *testing.T) {
	dir := t.TempDir()
	servers := map[string]config.MCPServerConfig{
		"github": {Command: "npx", Args: []string{"-y", "@anthropic/github-mcp-server"}},
	}

	if err := WriteMCPConfig(dir, servers); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}

	var out mcpJSON
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.MCPServers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(out.MCPServers))
	}
	entry, ok := out.MCPServers["github"]
	if !ok {
		t.Fatal("missing 'github' entry")
	}
	if entry.Command != "npx" {
		t.Errorf("command = %q, want %q", entry.Command, "npx")
	}
	if len(entry.Args) != 2 || entry.Args[0] != "-y" {
		t.Errorf("args = %v, want [-y @anthropic/github-mcp-server]", entry.Args)
	}
}

func TestWriteMCPConfig_MultipleServers(t *testing.T) {
	dir := t.TempDir()
	servers := map[string]config.MCPServerConfig{
		"unity":   {Command: "npx", Args: []string{"-y", "@anthropic/unity-mcp-server"}},
		"semgrep": {Command: "semgrep-mcp", Args: []string{"--config", "auto"}},
	}

	if err := WriteMCPConfig(dir, servers); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}

	var out mcpJSON
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.MCPServers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(out.MCPServers))
	}
	if _, ok := out.MCPServers["unity"]; !ok {
		t.Error("missing 'unity' entry")
	}
	if _, ok := out.MCPServers["semgrep"]; !ok {
		t.Error("missing 'semgrep' entry")
	}
}

func TestWriteMCPConfig_EmptyMap(t *testing.T) {
	dir := t.TempDir()

	if err := WriteMCPConfig(dir, nil); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, ".mcp.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected no .mcp.json for empty server map")
	}
}

func TestWriteMCPConfig_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	os.WriteFile(path, []byte(`{"old": true}`), 0644)

	servers := map[string]config.MCPServerConfig{
		"new-server": {Command: "test"},
	}
	if err := WriteMCPConfig(dir, servers); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var out mcpJSON
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out.MCPServers["new-server"]; !ok {
		t.Error("expected new-server entry after overwrite")
	}
}

func TestWriteMCPConfig_EnvVars(t *testing.T) {
	dir := t.TempDir()
	servers := map[string]config.MCPServerConfig{
		"api": {
			Command: "api-server",
			Args:    []string{"--port", "8080"},
			Env:     map[string]string{"API_KEY": "secret", "DEBUG": "true"},
		},
	}

	if err := WriteMCPConfig(dir, servers); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}

	var out mcpJSON
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	entry := out.MCPServers["api"]
	if entry.Env["API_KEY"] != "secret" {
		t.Errorf("env API_KEY = %q, want %q", entry.Env["API_KEY"], "secret")
	}
	if entry.Env["DEBUG"] != "true" {
		t.Errorf("env DEBUG = %q, want %q", entry.Env["DEBUG"], "true")
	}
}

func TestPrepareSkills_BuiltinCopy(t *testing.T) {
	wsDir := t.TempDir()
	builtinDir := t.TempDir()

	skillDir := filepath.Join(builtinDir, "owasp-checklist")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: owasp-checklist\ndescription: OWASP audit checklist\n---\n# OWASP\nCheck for injection."), 0644)

	refs := []string{"anthem://owasp-checklist"}
	if err := PrepareSkills(wsDir, refs, builtinDir, nil); err != nil {
		t.Fatal(err)
	}

	copied := filepath.Join(wsDir, ".claude", "skills", "owasp-checklist", "SKILL.md")
	data, err := os.ReadFile(copied)
	if err != nil {
		t.Fatalf("expected copied SKILL.md: %v", err)
	}
	if len(data) == 0 {
		t.Error("copied SKILL.md is empty")
	}
}

func TestPrepareSkills_ProjectLocalSkipped(t *testing.T) {
	wsDir := t.TempDir()
	refs := []string{"./skills/unity-patterns"}

	if err := PrepareSkills(wsDir, refs, "", nil); err != nil {
		t.Fatal(err)
	}

	skillsDir := filepath.Join(wsDir, ".claude", "skills")
	if _, err := os.Stat(skillsDir); !os.IsNotExist(err) {
		t.Error("expected no .claude/skills/ dir for project-local-only refs")
	}
}

func TestPrepareSkills_MixedRefs(t *testing.T) {
	wsDir := t.TempDir()
	builtinDir := t.TempDir()

	skillDir := filepath.Join(builtinDir, "pr-workflow")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: pr-workflow\n---\n"), 0644)

	refs := []string{"anthem://pr-workflow", "./skills/local-thing"}
	if err := PrepareSkills(wsDir, refs, builtinDir, nil); err != nil {
		t.Fatal(err)
	}

	// Built-in should be copied
	if _, err := os.Stat(filepath.Join(wsDir, ".claude", "skills", "pr-workflow", "SKILL.md")); err != nil {
		t.Error("expected built-in skill to be copied")
	}

	// Project-local should NOT create extra dirs
	if _, err := os.Stat(filepath.Join(wsDir, ".claude", "skills", "local-thing")); !os.IsNotExist(err) {
		t.Error("expected no copy for project-local skill ref")
	}
}

func TestPrepareSkills_MissingBuiltin(t *testing.T) {
	wsDir := t.TempDir()
	refs := []string{"anthem://nonexistent-skill"}

	err := PrepareSkills(wsDir, refs, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("expected no error for missing builtin, got: %v", err)
	}
}

func TestPrepareSkills_CreatesSkillsDir(t *testing.T) {
	wsDir := t.TempDir()
	builtinDir := t.TempDir()

	skillDir := filepath.Join(builtinDir, "test-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("test"), 0644)

	refs := []string{"anthem://test-skill"}
	if err := PrepareSkills(wsDir, refs, builtinDir, nil); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(wsDir, ".claude", "skills"))
	if err != nil {
		t.Fatal("expected .claude/skills/ to be created")
	}
	if !info.IsDir() {
		t.Error("expected .claude/skills/ to be a directory")
	}
}

func TestPrepareSkills_SkillHasSKILLmd(t *testing.T) {
	wsDir := t.TempDir()
	builtinDir := t.TempDir()

	content := "---\nname: security-audit\ndescription: Security audit checklist\n---\n# Steps\n1. Check auth\n2. Check injection"
	skillDir := filepath.Join(builtinDir, "security-audit")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644)

	refs := []string{"anthem://security-audit"}
	PrepareSkills(wsDir, refs, builtinDir, nil)

	data, err := os.ReadFile(filepath.Join(wsDir, ".claude", "skills", "security-audit", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("SKILL.md content mismatch:\ngot:  %q\nwant: %q", string(data), content)
	}
}

func TestPrepareSkills_EmptyRefs(t *testing.T) {
	wsDir := t.TempDir()

	if err := PrepareSkills(wsDir, nil, "", nil); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(wsDir, ".claude")); !os.IsNotExist(err) {
		t.Error("expected no .claude/ dir for empty refs")
	}
}

func TestResolveMCPServers_ProfileRefs(t *testing.T) {
	registry := map[string]config.MCPServerConfig{
		"unity":   {Command: "npx", Args: []string{"unity"}},
		"semgrep": {Command: "semgrep-mcp"},
		"github":  {Command: "npx", Args: []string{"github"}},
	}

	result := ResolveMCPServers(registry, nil, []string{"semgrep", "unity"})
	if len(result) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(result))
	}
	if _, ok := result["semgrep"]; !ok {
		t.Error("missing semgrep")
	}
	if _, ok := result["unity"]; !ok {
		t.Error("missing unity")
	}
}

func TestResolveMCPServers_UnknownRef(t *testing.T) {
	registry := map[string]config.MCPServerConfig{
		"unity": {Command: "npx"},
	}

	result := ResolveMCPServers(registry, nil, []string{"unity", "nonexistent"})
	if len(result) != 1 {
		t.Fatalf("expected 1 server (unknown ref skipped), got %d", len(result))
	}
}

func TestResolveSkillRefs_Deduplication(t *testing.T) {
	global := []string{"anthem://pr-workflow", "./skills/local"}
	profile := []string{"anthem://pr-workflow", "anthem://owasp"}

	result := ResolveSkillRefs(global, profile)
	if len(result) != 3 {
		t.Fatalf("expected 3 unique skills, got %d: %v", len(result), result)
	}
}

func TestResolveSkillRefs_EmptyInputs(t *testing.T) {
	result := ResolveSkillRefs(nil, nil)
	if len(result) != 0 {
		t.Errorf("expected empty result, got %v", result)
	}
}
