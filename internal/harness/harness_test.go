package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rauriemo/anthem/internal/config"
)

type testMCPJSON struct {
	MCPServers map[string]testMCPEntry `json:"mcpServers"`
}

type testMCPEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

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

	var out testMCPJSON
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

	var out testMCPJSON
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
	if err := os.WriteFile(path, []byte(`{"old": true}`), 0644); err != nil {
		t.Fatal(err)
	}

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

	var out testMCPJSON
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

	var out testMCPJSON
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
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: owasp-checklist\ndescription: OWASP audit checklist\n---\n# OWASP\nCheck for injection."), 0644); err != nil {
		t.Fatal(err)
	}

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
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: pr-workflow\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}

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
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

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
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	refs := []string{"anthem://security-audit"}
	if err := PrepareSkills(wsDir, refs, builtinDir, nil); err != nil {
		t.Fatal(err)
	}

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

func TestWriteEmbeddedSkill_CoreSkillsExist(t *testing.T) {
	coreSkills := []string{
		"test-verifier",
		"gh-issue-verifier",
		"code-review",
		"tdd-classicist",
		"go-cli",
		"commit-hygiene",
	}

	for _, name := range coreSkills {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), name)
			found, err := WriteEmbeddedSkill(name, dst)
			if err != nil {
				t.Fatalf("WriteEmbeddedSkill(%q) error: %v", name, err)
			}
			if !found {
				t.Fatalf("expected embedded skill %q to exist", name)
			}

			skillFile := filepath.Join(dst, "SKILL.md")
			data, err := os.ReadFile(skillFile)
			if err != nil {
				t.Fatalf("expected SKILL.md in %q: %v", name, err)
			}
			if len(data) < 100 {
				t.Errorf("SKILL.md for %q is suspiciously small (%d bytes)", name, len(data))
			}
		})
	}
}

func TestWriteEmbeddedSkill_NonexistentReturnsFalse(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "nonexistent")
	found, err := WriteEmbeddedSkill("does-not-exist", dst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected found=false for nonexistent skill")
	}
}

func TestWriteEmbeddedSkill_SkillMDHasFrontmatter(t *testing.T) {
	coreSkills := []string{
		"test-verifier",
		"gh-issue-verifier",
		"code-review",
		"tdd-classicist",
		"go-cli",
		"commit-hygiene",
	}

	for _, name := range coreSkills {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), name)
			if _, err := WriteEmbeddedSkill(name, dst); err != nil {
				t.Fatal(err)
			}

			data, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
			if err != nil {
				t.Fatal(err)
			}

			content := string(data)
			if content[:4] != "---\n" {
				t.Errorf("SKILL.md for %q should start with YAML frontmatter (---)", name)
			}
		})
	}
}

func TestPrepareSkills_EmbeddedFallback(t *testing.T) {
	wsDir := t.TempDir()
	refs := []string{"anthem://test-verifier"}

	if err := PrepareSkills(wsDir, refs, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}

	skillFile := filepath.Join(wsDir, ".claude", "skills", "test-verifier", "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("expected embedded skill to be extracted: %v", err)
	}
	if len(data) < 100 {
		t.Error("extracted SKILL.md is too small")
	}
}

func TestPrepareSkills_AllEmbeddedSkillsExtract(t *testing.T) {
	wsDir := t.TempDir()
	refs := []string{
		"anthem://test-verifier",
		"anthem://gh-issue-verifier",
		"anthem://code-review",
		"anthem://tdd-classicist",
		"anthem://go-cli",
		"anthem://commit-hygiene",
	}

	if err := PrepareSkills(wsDir, refs, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}

	for _, ref := range refs {
		name := ref[len("anthem://"):]
		skillFile := filepath.Join(wsDir, ".claude", "skills", name, "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			t.Errorf("expected %s to be extracted: %v", name, err)
		}
	}
}

func TestPrepareSkills_EmbeddedTakesPriorityOverFilesystem(t *testing.T) {
	wsDir := t.TempDir()
	builtinDir := t.TempDir()

	// Create a filesystem version with distinct content
	skillDir := filepath.Join(builtinDir, "test-verifier")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("filesystem version"), 0644); err != nil {
		t.Fatal(err)
	}

	refs := []string{"anthem://test-verifier"}
	if err := PrepareSkills(wsDir, refs, builtinDir, nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(wsDir, ".claude", "skills", "test-verifier", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}

	// Embedded version should win (it has YAML frontmatter)
	if string(data) == "filesystem version" {
		t.Error("expected embedded skill to take priority over filesystem")
	}
	if string(data[:3]) != "---" {
		t.Error("expected embedded skill YAML frontmatter")
	}
}

func TestPrepareSkills_MixedEmbeddedAndLocal(t *testing.T) {
	wsDir := t.TempDir()
	refs := []string{
		"anthem://code-review",
		"./skills/my-project-skill",
		"anthem://commit-hygiene",
	}

	if err := PrepareSkills(wsDir, refs, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}

	// Embedded skills should be extracted
	if _, err := os.Stat(filepath.Join(wsDir, ".claude", "skills", "code-review", "SKILL.md")); err != nil {
		t.Error("expected code-review to be extracted")
	}
	if _, err := os.Stat(filepath.Join(wsDir, ".claude", "skills", "commit-hygiene", "SKILL.md")); err != nil {
		t.Error("expected commit-hygiene to be extracted")
	}

	// Project-local should NOT be copied
	if _, err := os.Stat(filepath.Join(wsDir, ".claude", "skills", "my-project-skill")); !os.IsNotExist(err) {
		t.Error("project-local skill should not be copied")
	}
}

func TestResolveMCPServers_GlobalRefs(t *testing.T) {
	registry := map[string]config.MCPServerConfig{
		"unity":   {Command: "npx", Args: []string{"unity"}},
		"semgrep": {Command: "semgrep-mcp"},
		"github":  {Command: "npx", Args: []string{"github"}},
	}

	result := ResolveMCPServers(registry, []string{"unity", "github"}, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 servers from globalRefs, got %d", len(result))
	}
	if _, ok := result["unity"]; !ok {
		t.Error("missing unity from global refs")
	}
	if _, ok := result["github"]; !ok {
		t.Error("missing github from global refs")
	}
	if _, ok := result["semgrep"]; ok {
		t.Error("semgrep should not be included (not in globalRefs)")
	}
}

func TestResolveMCPServers_GlobalAndProfileMerge(t *testing.T) {
	registry := map[string]config.MCPServerConfig{
		"unity":   {Command: "npx", Args: []string{"unity"}},
		"semgrep": {Command: "semgrep-mcp"},
		"github":  {Command: "npx", Args: []string{"github"}},
	}

	result := ResolveMCPServers(registry, []string{"github"}, []string{"semgrep"})
	if len(result) != 2 {
		t.Fatalf("expected 2 servers (1 global + 1 profile), got %d", len(result))
	}
	if _, ok := result["github"]; !ok {
		t.Error("missing github from global refs")
	}
	if _, ok := result["semgrep"]; !ok {
		t.Error("missing semgrep from profile refs")
	}
}

func TestResolveMCPServers_EmptyRegistry(t *testing.T) {
	result := ResolveMCPServers(nil, []string{"anything"}, []string{"whatever"})
	if len(result) != 0 {
		t.Errorf("expected empty result from nil registry, got %d", len(result))
	}
}

func TestResolveMCPServers_EmptyRefs(t *testing.T) {
	registry := map[string]config.MCPServerConfig{
		"unity": {Command: "npx"},
	}
	result := ResolveMCPServers(registry, nil, nil)
	if len(result) != 0 {
		t.Errorf("expected empty result with no refs, got %d", len(result))
	}
}

func TestWriteEmbeddedSkill_ContentPreserved(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "code-review")
	found, err := WriteEmbeddedSkill("code-review", dst)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected code-review to exist")
	}

	data, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	// Verify key content sections are preserved
	if !contains(content, "name: code-review") {
		t.Error("missing frontmatter name field")
	}
	if !contains(content, "## When to use this skill") {
		t.Error("missing 'When to use' section")
	}
	if !contains(content, "## When NOT to use") {
		t.Error("missing 'When NOT to use' section")
	}
	if !contains(content, "## Output contract") {
		t.Error("missing output contract section")
	}
}

func TestWriteEmbeddedSkill_AllSkillsHaveRequiredSections(t *testing.T) {
	coreSkills := []string{
		"test-verifier",
		"gh-issue-verifier",
		"code-review",
		"tdd-classicist",
		"go-cli",
		"commit-hygiene",
	}

	requiredSections := []string{
		"## When to use",
		"## When NOT to use",
	}

	for _, name := range coreSkills {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), name)
			if _, err := WriteEmbeddedSkill(name, dst); err != nil {
				t.Fatal(err)
			}

			data, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
			if err != nil {
				t.Fatal(err)
			}

			content := string(data)
			for _, section := range requiredSections {
				if !contains(content, section) {
					t.Errorf("skill %q missing required section %q", name, section)
				}
			}
		})
	}
}

func TestWriteEmbeddedSkill_AllSkillsHaveCompositionTable(t *testing.T) {
	coreSkills := []string{
		"test-verifier",
		"gh-issue-verifier",
		"code-review",
		"tdd-classicist",
		"commit-hygiene",
	}

	for _, name := range coreSkills {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), name)
			if _, err := WriteEmbeddedSkill(name, dst); err != nil {
				t.Fatal(err)
			}

			data, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
			if err != nil {
				t.Fatal(err)
			}

			if !contains(string(data), "## Composition with other skills") {
				t.Errorf("skill %q missing composition table", name)
			}
		})
	}
}

func TestResolveSkillRefs_ProfileOnly(t *testing.T) {
	result := ResolveSkillRefs(nil, []string{"anthem://test-verifier", "anthem://code-review"})
	if len(result) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(result))
	}
	if result[0] != "anthem://test-verifier" {
		t.Errorf("result[0] = %q, want anthem://test-verifier", result[0])
	}
}

func TestResolveSkillRefs_GlobalOnly(t *testing.T) {
	result := ResolveSkillRefs([]string{"anthem://code-review", "anthem://gh-issue-verifier"}, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(result))
	}
}

func TestResolveSkillRefs_PreservesOrder(t *testing.T) {
	global := []string{"anthem://a", "anthem://b"}
	profile := []string{"anthem://c", "anthem://d"}

	result := ResolveSkillRefs(global, profile)
	expected := []string{"anthem://a", "anthem://b", "anthem://c", "anthem://d"}
	if len(result) != len(expected) {
		t.Fatalf("expected %d skills, got %d", len(expected), len(result))
	}
	for i, want := range expected {
		if result[i] != want {
			t.Errorf("result[%d] = %q, want %q", i, result[i], want)
		}
	}
}

func TestMergeGuestServers_GuestOverridesGlobal(t *testing.T) {
	global := map[string]config.MCPServerConfig{
		"unity": {Command: "npx", Args: []string{"unity-global"}},
	}
	guest := map[string]config.MCPServerConfig{
		"unity":   {Command: "node", Args: []string{"unity-local"}},
		"semgrep": {Command: "semgrep-mcp"},
	}

	result := MergeGuestServers(global, guest)
	if len(result) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(result))
	}
	if result["unity"].Command != "node" {
		t.Errorf("guest should override global: command = %q, want node", result["unity"].Command)
	}
	if _, ok := result["semgrep"]; !ok {
		t.Error("missing semgrep from guest servers")
	}
}

func TestMergeGuestServers_BothEmpty(t *testing.T) {
	result := MergeGuestServers(nil, nil)
	if result != nil {
		t.Errorf("expected nil for both empty, got %v", result)
	}
}

func TestMergeGuestServers_GlobalOnly(t *testing.T) {
	global := map[string]config.MCPServerConfig{
		"unity": {Command: "npx"},
	}
	result := MergeGuestServers(global, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 server, got %d", len(result))
	}
}

func TestMergeGuestServers_GuestOnly(t *testing.T) {
	guest := map[string]config.MCPServerConfig{
		"custom": {Command: "my-server"},
	}
	result := MergeGuestServers(nil, guest)
	if len(result) != 1 {
		t.Fatalf("expected 1 server, got %d", len(result))
	}
}

func TestWriteMCPConfig_TypeMigration(t *testing.T) {
	dir := t.TempDir()
	servers := map[string]config.MCPServerConfig{
		"legacy": {Command: "npx", Args: []string{"-y", "mcp-server"}},
	}

	if err := WriteMCPConfig(dir, servers); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}

	var out testMCPJSON
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	entry, ok := out.MCPServers["legacy"]
	if !ok {
		t.Fatal("missing legacy server")
	}
	if entry.Command != "npx" {
		t.Errorf("command = %q, want npx", entry.Command)
	}
}

func TestWriteMCPConfig_MixedTransports(t *testing.T) {
	dir := t.TempDir()
	servers := map[string]config.MCPServerConfig{
		"mcp-unity": {
			Type:    "stdio",
			Command: "node",
			Args:    []string{"server.js"},
		},
		"remote": {
			Type:    "http",
			URL:     "http://localhost:9090/mcp",
			Headers: map[string]string{"X-Api-Key": "test123"},
		},
	}

	if err := WriteMCPConfig(dir, servers); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}

	var out testMCPJSON
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}

	if len(out.MCPServers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(out.MCPServers))
	}

	stdio := out.MCPServers["mcp-unity"]
	if stdio.Command != "node" {
		t.Errorf("stdio command = %q, want node", stdio.Command)
	}

	http := out.MCPServers["remote"]
	if http.URL != "http://localhost:9090/mcp" {
		t.Errorf("http url = %q, want http://localhost:9090/mcp", http.URL)
	}
	if http.Headers["X-Api-Key"] != "test123" {
		t.Errorf("http header X-Api-Key = %q, want test123", http.Headers["X-Api-Key"])
	}
}

func TestWriteMCPConfig_EmptyNonNilMap(t *testing.T) {
	dir := t.TempDir()
	servers := map[string]config.MCPServerConfig{}

	if err := WriteMCPConfig(dir, servers); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, ".mcp.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected no file for empty non-nil map")
	}
}

func TestMergeGuestServers_CrossTransportOverride(t *testing.T) {
	global := map[string]config.MCPServerConfig{
		"server": {Type: "stdio", Command: "npx", Args: []string{"old"}},
	}
	guest := map[string]config.MCPServerConfig{
		"server": {Type: "http", URL: "http://localhost:8080/mcp"},
	}

	result := MergeGuestServers(global, guest)
	if result["server"].Type != "http" {
		t.Errorf("expected guest HTTP to override global stdio, got type = %q", result["server"].Type)
	}
	if result["server"].URL != "http://localhost:8080/mcp" {
		t.Errorf("expected guest URL, got %q", result["server"].URL)
	}
	if result["server"].Command != "" {
		t.Errorf("expected command to be empty after override, got %q", result["server"].Command)
	}
}

func TestMergeGuestServers_EmptyNonNilMaps(t *testing.T) {
	result := MergeGuestServers(
		map[string]config.MCPServerConfig{},
		map[string]config.MCPServerConfig{},
	)
	if result != nil {
		t.Errorf("expected nil for two empty non-nil maps, got %v", result)
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
