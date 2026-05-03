package guests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		check   func(t *testing.T, agent GuestAgent)
	}{
		{
			name:  "full frontmatter",
			input: readFixture(t, "game-designer.md"),
			check: func(t *testing.T, agent GuestAgent) {
				if agent.Name != "Game Story Weaver" {
					t.Errorf("name = %q, want %q", agent.Name, "Game Story Weaver")
				}
				if agent.Role != "specialist" {
					t.Errorf("role = %q, want %q", agent.Role, "specialist")
				}
				if agent.Icon != "book" {
					t.Errorf("icon = %q, want %q", agent.Icon, "book")
				}
				if agent.Model != "claude-opus-4-6" {
					t.Errorf("model = %q, want %q", agent.Model, "claude-opus-4-6")
				}
				if len(agent.Capabilities) != 3 {
					t.Errorf("capabilities count = %d, want 3", len(agent.Capabilities))
				}
				if agent.RequirementsFingerprint == "" {
					t.Error("expected non-empty requirements fingerprint")
				}
			},
		},
		{
			name:  "minimal frontmatter",
			input: readFixture(t, "code-reviewer.md"),
			check: func(t *testing.T, agent GuestAgent) {
				if agent.Name != "Code Reviewer" {
					t.Errorf("name = %q, want %q", agent.Name, "Code Reviewer")
				}
				if agent.Role != "" {
					t.Errorf("role = %q, want empty", agent.Role)
				}
				if agent.Icon != "" {
					t.Errorf("icon = %q, want empty", agent.Icon)
				}
			},
		},
		{
			name:    "missing name",
			input:   readFixture(t, "incomplete.md"),
			wantErr: true,
		},
		{
			name:    "malformed YAML",
			input:   readFixture(t, "malformed.md"),
			wantErr: true,
		},
		{
			name:  "valid frontmatter no body",
			input: readFixture(t, "no-body.md"),
			check: func(t *testing.T, agent GuestAgent) {
				if agent.Name != "Empty Agent" {
					t.Errorf("name = %q, want %q", agent.Name, "Empty Agent")
				}
			},
		},
		{
			name:    "empty file",
			input:   "",
			wantErr: true,
		},
		{
			name:    "missing closing delimiter",
			input:   "---\nname: Test\ndescription: Missing close\n",
			wantErr: true,
		},
		{
			name:    "missing opening delimiter",
			input:   "name: Test\ndescription: No delimiters\n---\n",
			wantErr: true,
		},
		{
			name:    "missing description",
			input:   "---\nname: OnlyName\n---\n",
			wantErr: true,
		},
		{
			name:  "extra unknown fields ignored",
			input: "---\nname: Agent\ndescription: Has extras\nunknown_field: value\nanother: 42\n---\nBody.",
			check: func(t *testing.T, agent GuestAgent) {
				if agent.Name != "Agent" {
					t.Errorf("name = %q, want %q", agent.Name, "Agent")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, err := ParseFrontmatter([]byte(tt.input))
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
				tt.check(t, agent)
			}
		})
	}
}

func TestScanDirectory(t *testing.T) {
	tests := []struct {
		name      string
		dir       string
		setup     func(t *testing.T) string
		wantErr   bool
		wantCount int
		wantSlugs []string
	}{
		{
			name: "normal directory with valid and invalid files",
			setup: func(t *testing.T) string {
				return fixtureDir(t)
			},
			wantCount: 5, // game-designer, code-reviewer, no-body, tooled-agent, async-agent (incomplete + malformed + bad-auth-scheme skipped)
			wantSlugs: []string{"game-designer", "code-reviewer", "no-body", "tooled-agent", "async-agent"},
		},
		{
			name: "empty directory",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				return dir
			},
			wantCount: 0,
		},
		{
			name: "nonexistent directory",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent")
			},
			wantErr: true,
		},
		{
			name: "only non-md files",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "data.json"), []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setup(t)
			index, err := ScanDirectory(dir, nil)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(index.Agents) != tt.wantCount {
				t.Errorf("agent count = %d, want %d (agents: %v)", len(index.Agents), tt.wantCount, agentKeys(index))
			}
			for _, slug := range tt.wantSlugs {
				if _, ok := index.Agents[slug]; !ok {
					t.Errorf("missing expected agent %q", slug)
				}
			}
			if index.Version != 1 {
				t.Errorf("version = %d, want 1", index.Version)
			}
		})
	}
}

func TestWriteAndLoadIndex(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	original := GuestIndex{
		Version:     1,
		GeneratedAt: now,
		Agents: map[string]GuestAgent{
			"test-agent": {
				ID:          "test-agent",
				Name:        "Test Agent",
				Description: "A test",
				Scope:       "project",
				Source:      "local",
				File:        "test-agent.md",
			},
		},
	}

	if err := WriteIndex(dir, original); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}

	loaded, err := LoadIndex(dir)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}

	if loaded.Version != original.Version {
		t.Errorf("version = %d, want %d", loaded.Version, original.Version)
	}
	if len(loaded.Agents) != 1 {
		t.Fatalf("agent count = %d, want 1", len(loaded.Agents))
	}
	agent := loaded.Agents["test-agent"]
	if agent.Name != "Test Agent" {
		t.Errorf("name = %q, want %q", agent.Name, "Test Agent")
	}
}

func TestLoadIndexMissing(t *testing.T) {
	_, err := LoadIndex(t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing index file")
	}
}

func TestLoadIndexCorrupted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, indexFile), []byte("{invalid json"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadIndex(dir)
	if err == nil {
		t.Fatal("expected error for corrupted index file")
	}
}

func TestWriteIndexAtomic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, indexFile)

	// Write a valid index first
	original := GuestIndex{Version: 1, GeneratedAt: time.Now().UTC(), Agents: map[string]GuestAgent{
		"a": {ID: "a", Name: "A", Description: "a"},
	}}
	if err := WriteIndex(dir, original); err != nil {
		t.Fatalf("initial write: %v", err)
	}

	// Verify tmp file doesn't linger
	_, err := os.Stat(target + ".tmp")
	if err == nil {
		t.Error("temp file should not exist after successful write")
	}
}

func TestGeneratedAtIsRecent(t *testing.T) {
	dir := t.TempDir()
	before := time.Now().UTC()
	index := GuestIndex{Version: 1, GeneratedAt: time.Now().UTC(), Agents: map[string]GuestAgent{}}
	if err := WriteIndex(dir, index); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}
	loaded, err := LoadIndex(dir)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if loaded.GeneratedAt.Before(before.Add(-time.Second)) {
		t.Errorf("generated_at %v is too old", loaded.GeneratedAt)
	}
}

func TestLoadPersona(t *testing.T) {
	tests := []struct {
		name    string
		slug    string
		wantErr bool
		check   func(t *testing.T, body string)
	}{
		{
			name: "full body",
			slug: "game-designer",
			check: func(t *testing.T, body string) {
				if body == "" {
					t.Error("expected non-empty body")
				}
				if len(body) < 50 {
					t.Errorf("body too short: %q", body)
				}
			},
		},
		{
			name: "empty body",
			slug: "no-body",
			check: func(t *testing.T, body string) {
				if body != "" {
					t.Errorf("expected empty body, got %q", body)
				}
			},
		},
		{
			name:    "nonexistent agent",
			slug:    "does-not-exist",
			wantErr: true,
		},
	}

	dir := fixtureDir(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := LoadPersona(dir, tt.slug)
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
				tt.check(t, body)
			}
		})
	}
}

func TestComputeRequirementsFingerprint(t *testing.T) {
	tests := []struct {
		name string
		a, b map[string]any
		same bool
	}{
		{
			name: "deterministic same input",
			a:    map[string]any{"internet": true, "filesystem": "read-write"},
			b:    map[string]any{"internet": true, "filesystem": "read-write"},
			same: true,
		},
		{
			name: "field order independent",
			a:    map[string]any{"filesystem": "read-write", "internet": true},
			b:    map[string]any{"internet": true, "filesystem": "read-write"},
			same: true,
		},
		{
			name: "different values",
			a:    map[string]any{"internet": true},
			b:    map[string]any{"internet": false},
			same: false,
		},
		{
			name: "nil vs empty",
			a:    nil,
			b:    map[string]any{},
			same: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fa := ComputeRequirementsFingerprint(tt.a)
			fb := ComputeRequirementsFingerprint(tt.b)

			if !hasPrefix(fa, "sha256:") {
				t.Errorf("fingerprint %q missing sha256: prefix", fa)
			}

			if tt.same && fa != fb {
				t.Errorf("expected same fingerprint, got %q and %q", fa, fb)
			}
			if !tt.same && fa == fb {
				t.Errorf("expected different fingerprints, got same: %q", fa)
			}
		})
	}
}

func TestFilterByAllowlist(t *testing.T) {
	index := GuestIndex{
		Version:     1,
		GeneratedAt: time.Now().UTC(),
		Agents: map[string]GuestAgent{
			"alpha": {ID: "alpha", Name: "Alpha"},
			"beta":  {ID: "beta", Name: "Beta"},
			"gamma": {ID: "gamma", Name: "Gamma"},
		},
	}

	tests := []struct {
		name      string
		allowlist []string
		wantCount int
		wantSlugs []string
	}{
		{
			name:      "nil allowlist passes all",
			allowlist: nil,
			wantCount: 3,
		},
		{
			name:      "empty allowlist filters all",
			allowlist: []string{},
			wantCount: 0,
		},
		{
			name:      "subset",
			allowlist: []string{"alpha", "gamma"},
			wantCount: 2,
			wantSlugs: []string{"alpha", "gamma"},
		},
		{
			name:      "nonexistent slug ignored",
			allowlist: []string{"alpha", "nonexistent"},
			wantCount: 1,
			wantSlugs: []string{"alpha"},
		},
		{
			name:      "duplicates deduplicated",
			allowlist: []string{"alpha", "alpha", "beta"},
			wantCount: 2,
			wantSlugs: []string{"alpha", "beta"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterByAllowlist(index, tt.allowlist)
			if len(result.Agents) != tt.wantCount {
				t.Errorf("count = %d, want %d", len(result.Agents), tt.wantCount)
			}
			for _, slug := range tt.wantSlugs {
				if _, ok := result.Agents[slug]; !ok {
					t.Errorf("missing expected agent %q", slug)
				}
			}
		})
	}
}

func TestScanDirectoryAgentFields(t *testing.T) {
	dir := fixtureDir(t)
	index, err := ScanDirectory(dir, nil)
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}

	agent, ok := index.Agents["game-designer"]
	if !ok {
		t.Fatal("game-designer not found in index")
	}

	if agent.ID != "game-designer" {
		t.Errorf("ID = %q, want %q", agent.ID, "game-designer")
	}
	if agent.File != "game-designer.md" {
		t.Errorf("File = %q, want %q", agent.File, "game-designer.md")
	}
	if agent.Scope != "project" {
		t.Errorf("Scope = %q, want %q", agent.Scope, "project")
	}
	if agent.Source != "local" {
		t.Errorf("Source = %q, want %q", agent.Source, "local")
	}
}

func TestIndexRoundTripJSON(t *testing.T) {
	original := GuestIndex{
		Version:     1,
		GeneratedAt: time.Now().UTC().Truncate(time.Second),
		Agents: map[string]GuestAgent{
			"test": {
				ID:                      "test",
				Name:                    "Test",
				Description:             "A test agent",
				Role:                    "specialist",
				Capabilities:            []string{"testing"},
				Icon:                    "beaker",
				Model:                   "claude-opus-4-6",
				RequirementsFingerprint: "sha256:abc123",
				Scope:                   "project",
				Source:                  "local",
				File:                    "test.md",
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var loaded GuestIndex
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if loaded.Agents["test"].Name != "Test" {
		t.Errorf("name = %q, want %q", loaded.Agents["test"].Name, "Test")
	}
	if loaded.Agents["test"].Role != "specialist" {
		t.Errorf("role = %q, want %q", loaded.Agents["test"].Role, "specialist")
	}
}

func TestParseFrontmatterBodyWithHorizontalRule(t *testing.T) {
	input := "---\nname: Agent\ndescription: test\n---\n\nBefore rule\n\n---\n\nAfter rule\n"
	agent, err := ParseFrontmatter([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.Name != "Agent" {
		t.Errorf("name = %q, want %q", agent.Name, "Agent")
	}
	_, body, err := splitFrontmatterDelimiters([]byte(input))
	if err != nil {
		t.Fatalf("splitFrontmatterDelimiters: %v", err)
	}
	if !contains(body, "Before rule") || !contains(body, "After rule") {
		t.Errorf("body missing content around ---: %q", body)
	}
}

func TestScanDirectorySingleAgent(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: Solo\ndescription: Only agent\n---\n\nI'm alone.\n"
	if err := os.WriteFile(filepath.Join(dir, "solo.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	index, err := ScanDirectory(dir, nil)
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if len(index.Agents) != 1 {
		t.Fatalf("agent count = %d, want 1", len(index.Agents))
	}
	agent := index.Agents["solo"]
	if agent.ID != "solo" {
		t.Errorf("ID = %q, want %q", agent.ID, "solo")
	}
	if agent.Scope != "project" {
		t.Errorf("Scope = %q, want %q", agent.Scope, "project")
	}
	if agent.Source != "local" {
		t.Errorf("Source = %q, want %q", agent.Source, "local")
	}
}

func TestParseFrontmatter_ToolPolicyFields(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
		check   func(t *testing.T, agent GuestAgent)
	}{
		{
			name:  "parses allowed_tools, mcp_servers, http_tools",
			input: readFixture(t, "tooled-agent.md"),
			check: func(t *testing.T, agent GuestAgent) {
				if len(agent.AllowedTools) != 3 {
					t.Fatalf("AllowedTools count = %d, want 3", len(agent.AllowedTools))
				}
				if agent.AllowedTools[0] != "mcp__mcp-unity__*" {
					t.Errorf("AllowedTools[0] = %q", agent.AllowedTools[0])
				}
				if agent.AllowedTools[2] != "WebSearch" {
					t.Errorf("AllowedTools[2] = %q", agent.AllowedTools[2])
				}
				if len(agent.MCPServers) != 1 {
					t.Fatalf("MCPServers count = %d, want 1", len(agent.MCPServers))
				}
				srv := agent.MCPServers["mcp-unity"]
				if srv.Command != "npx" {
					t.Errorf("MCPServers[mcp-unity].Command = %q", srv.Command)
				}
				if len(srv.Args) != 2 || srv.Args[0] != "-y" {
					t.Errorf("MCPServers[mcp-unity].Args = %v", srv.Args)
				}
				if srv.Env["UNITY_PORT"] != "8080" {
					t.Errorf("MCPServers[mcp-unity].Env = %v", srv.Env)
				}
				if len(agent.HTTPTools) != 1 {
					t.Fatalf("HTTPTools count = %d, want 1", len(agent.HTTPTools))
				}
				ht := agent.HTTPTools["image_gen"]
				if ht.URL != "https://api.example.com/generate" {
					t.Errorf("HTTPTools[image_gen].URL = %q", ht.URL)
				}
				if ht.Method != "POST" {
					t.Errorf("HTTPTools[image_gen].Method = %q", ht.Method)
				}
				if ht.AuthTokenEnv != "IMAGE_API_KEY" {
					t.Errorf("HTTPTools[image_gen].AuthTokenEnv = %q", ht.AuthTokenEnv)
				}
				if ht.AuthScheme != "bearer" {
					t.Errorf("HTTPTools[image_gen].AuthScheme = %q", ht.AuthScheme)
				}
				if ht.TimeoutMS != 30000 {
					t.Errorf("HTTPTools[image_gen].TimeoutMS = %d", ht.TimeoutMS)
				}
				if ht.ResponseArtifact == nil {
					t.Fatal("HTTPTools[image_gen].ResponseArtifact is nil")
				}
				if ht.ResponseArtifact.Type != "image/png" {
					t.Errorf("ResponseArtifact.Type = %q", ht.ResponseArtifact.Type)
				}
			},
		},
		{
			name:    "rejects invalid auth_scheme",
			input:   readFixture(t, "bad-auth-scheme.md"),
			wantErr: true,
			errMsg:  "auth_scheme",
		},
		{
			name:  "api-key auth_scheme is valid",
			input: "---\nname: Agent\ndescription: test\nhttp_tools:\n  t:\n    url: http://x\n    method: POST\n    auth_scheme: api-key\n    auth_token_env: MY_KEY\n---\n",
			check: func(t *testing.T, agent GuestAgent) {
				if agent.HTTPTools["t"].AuthScheme != "api-key" {
					t.Errorf("expected api-key auth_scheme, got %q", agent.HTTPTools["t"].AuthScheme)
				}
			},
		},
		{
			name:  "empty auth_scheme is valid",
			input: "---\nname: Agent\ndescription: test\nhttp_tools:\n  t:\n    url: http://x\n    method: GET\n---\n",
			check: func(t *testing.T, agent GuestAgent) {
				if agent.HTTPTools["t"].AuthScheme != "" {
					t.Errorf("expected empty auth_scheme, got %q", agent.HTTPTools["t"].AuthScheme)
				}
			},
		},
		{
			name:  "no tool fields is valid",
			input: "---\nname: Plain\ndescription: no tools\n---\n",
			check: func(t *testing.T, agent GuestAgent) {
				if agent.AllowedTools != nil {
					t.Errorf("expected nil AllowedTools, got %v", agent.AllowedTools)
				}
				if agent.MCPServers != nil {
					t.Errorf("expected nil MCPServers, got %v", agent.MCPServers)
				}
				if agent.HTTPTools != nil {
					t.Errorf("expected nil HTTPTools, got %v", agent.HTTPTools)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, err := ParseFrontmatter([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, agent)
			}
		})
	}
}

func TestScanDirectory_SkipsInvalidAuthScheme(t *testing.T) {
	dir := fixtureDir(t)
	index, err := ScanDirectory(dir, nil)
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if _, ok := index.Agents["bad-auth-scheme"]; ok {
		t.Error("bad-auth-scheme agent should be skipped due to invalid auth_scheme")
	}
}

func TestFilterByAllowlistPreservesMetadata(t *testing.T) {
	now := time.Now().UTC()
	index := GuestIndex{
		Version:     1,
		GeneratedAt: now,
		Agents: map[string]GuestAgent{
			"alpha": {ID: "alpha", Name: "Alpha"},
		},
	}
	result := FilterByAllowlist(index, []string{"alpha"})
	if result.Version != 1 {
		t.Errorf("Version = %d, want 1", result.Version)
	}
	if !result.GeneratedAt.Equal(now) {
		t.Errorf("GeneratedAt changed")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstring(s, substr))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// helpers

func fixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "agents")
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "agents", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return string(data)
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func agentKeys(index GuestIndex) []string {
	keys := make([]string, 0, len(index.Agents))
	for k := range index.Agents {
		keys = append(keys, k)
	}
	return keys
}

func TestScanDirectory_ExcludesOrchestratorMD(t *testing.T) {
	dir := t.TempDir()

	orchContent := `---
name: My Project
description: Project orchestrator
role: orchestrator
---

## Identity
I am the orchestrator.
`
	guestContent := `---
name: Miyazaki
description: Game designer
---

You are a game designer.
`
	if err := os.WriteFile(filepath.Join(dir, "orchestrator.md"), []byte(orchContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "miyazaki.md"), []byte(guestContent), 0644); err != nil {
		t.Fatal(err)
	}

	index, err := ScanDirectory(dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := index.Agents["orchestrator"]; ok {
		t.Error("orchestrator.md should be excluded from GuestIndex")
	}
	if _, ok := index.Agents["miyazaki"]; !ok {
		t.Error("miyazaki.md should be in GuestIndex")
	}
	if len(index.Agents) != 1 {
		t.Errorf("expected exactly 1 agent in index, got %d", len(index.Agents))
	}
}

func TestScanDirectory_OrchestratorOnlyDir(t *testing.T) {
	dir := t.TempDir()

	orchContent := `---
name: Solo Project
description: Orchestrator only
role: orchestrator
---

## Identity
Alone.
`
	if err := os.WriteFile(filepath.Join(dir, "orchestrator.md"), []byte(orchContent), 0644); err != nil {
		t.Fatal(err)
	}

	index, err := ScanDirectory(dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(index.Agents) != 0 {
		t.Errorf("expected empty index when only orchestrator.md exists, got %d agents", len(index.Agents))
	}
}

func TestLoadOrchestratorPersona_Exists(t *testing.T) {
	dir := t.TempDir()

	content := `---
name: Test
description: Test orchestrator
role: orchestrator
---

## Identity
I am the test orchestrator.

## Personality
- Direct and clear.
`
	if err := os.WriteFile(filepath.Join(dir, "orchestrator.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	body, err := LoadOrchestratorPersona(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "## Identity") {
		t.Error("body should contain ## Identity section")
	}
	if !strings.Contains(body, "## Personality") {
		t.Error("body should contain ## Personality section")
	}
	if strings.Contains(body, "---") {
		t.Error("body should not contain frontmatter delimiters")
	}
}

func TestLoadOrchestratorPersona_Missing(t *testing.T) {
	dir := t.TempDir()

	body, err := LoadOrchestratorPersona(dir)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if body != "" {
		t.Errorf("expected empty body for missing file, got: %q", body)
	}
}

func TestLoadOrchestratorPersona_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	// File without frontmatter -- LoadPersona will return error, LoadOrchestratorPersona wraps it
	if err := os.WriteFile(filepath.Join(dir, "orchestrator.md"), []byte("Just plain text"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadOrchestratorPersona(dir)
	// This should return an error since the file exists but has no frontmatter
	if err == nil {
		t.Error("expected error for file without frontmatter")
	}
}

func TestReadOrchestratorAgent_FromOrchestratorMd(t *testing.T) {
	dir := t.TempDir()
	body := "---\n" +
		"name: Tower\n" +
		"role: orchestrator\n" +
		"description: Project coordinator.\n" +
		"model: claude-opus-5\n" +
		"max_turns: 16\n" +
		"---\n\nTower body.\n"
	if err := os.WriteFile(filepath.Join(dir, "orchestrator.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	agent, err := ReadOrchestratorAgent(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.ID != OrchestratorGuestID {
		t.Errorf("ID = %q, want %q (default when frontmatter omits id)", agent.ID, OrchestratorGuestID)
	}
	if agent.Name != "Tower" {
		t.Errorf("Name = %q, want Tower", agent.Name)
	}
	if agent.Role != "orchestrator" {
		t.Errorf("Role = %q, want orchestrator", agent.Role)
	}
	if agent.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want claude-opus-5", agent.Model)
	}
	if agent.MaxTurns != 16 {
		t.Errorf("MaxTurns = %d, want 16", agent.MaxTurns)
	}
}

func TestReadOrchestratorAgent_FallsBackToRoleScan(t *testing.T) {
	// When agents/orchestrator.md is absent, any .md with
	// role: orchestrator must be picked up so projects that name their
	// orchestrator file after its character (e.g. agents/tower.md) still
	// dispatch correctly.
	dir := t.TempDir()
	body := "---\n" +
		"name: Tower\n" +
		"role: orchestrator\n" +
		"description: Picked up via fallback scan.\n" +
		"---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "tower.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	agent, err := ReadOrchestratorAgent(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.Name != "Tower" {
		t.Errorf("Name = %q, want Tower (from fallback scan)", agent.Name)
	}
	if agent.Role != "orchestrator" {
		t.Errorf("Role = %q, want orchestrator", agent.Role)
	}
}

func TestReadOrchestratorAgent_MissingReturnsZero(t *testing.T) {
	// Fresh project with no agents directory at all: caller treats the
	// zero GuestAgent as "no dispatch override" and layers defaults. No
	// error is returned because absence is a valid state.
	dir := t.TempDir()
	agent, err := ReadOrchestratorAgent(dir)
	if err != nil {
		t.Fatalf("unexpected error for missing orchestrator: %v", err)
	}
	if agent.Name != "" || agent.Role != "" || agent.ID != "" {
		t.Errorf("expected zero GuestAgent for missing orchestrator; got %+v", agent)
	}
}

func TestReadOrchestratorAgent_MalformedOrchestratorMdErrors(t *testing.T) {
	// When orchestrator.md exists but has broken YAML, surface the error
	// instead of silently falling through to scan — the file is explicit
	// intent and a malformed one deserves a clear signal.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orchestrator.md"), []byte("no frontmatter here"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ReadOrchestratorAgent(dir)
	if err == nil {
		t.Error("expected error for orchestrator.md without frontmatter")
	}
}

func TestLoadAgentFrontmatter_Exists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.md")
	content := "---\nname: TestAgent\nrole: artist\ndescription: A test artist\n---\n\n## Identity\nBody text\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	fm, err := LoadAgentFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}
	if fm["name"] != "TestAgent" {
		t.Errorf("name = %v, want TestAgent", fm["name"])
	}
	if fm["role"] != "artist" {
		t.Errorf("role = %v, want artist", fm["role"])
	}
}

func TestLoadAgentFrontmatter_Missing(t *testing.T) {
	fm, err := LoadAgentFrontmatter("/nonexistent/path/agent.md")
	if err != nil {
		t.Fatal("should not error for missing file")
	}
	if len(fm) != 0 {
		t.Errorf("expected empty map, got %v", fm)
	}
}

func TestLoadAgentFrontmatter_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.md")
	if err := os.WriteFile(path, []byte("Just plain text, no frontmatter"), 0644); err != nil {
		t.Fatal(err)
	}

	fm, err := LoadAgentFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(fm) != 0 {
		t.Errorf("expected empty map for no frontmatter, got %v", fm)
	}
}

func TestLoadAgentFrontmatter_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.md")
	content := "---\nname: [invalid yaml\n  broken: {{\n---\n\nBody\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadAgentFrontmatter(path)
	if err == nil {
		t.Error("expected error for malformed YAML")
	}
}

func TestParseFrontmatter_AsyncPolling(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(t *testing.T, agent GuestAgent)
	}{
		{
			name:  "parses async_polling from fixture",
			input: readFixture(t, "async-agent.md"),
			check: func(t *testing.T, agent GuestAgent) {
				if agent.Name != "Async Agent" {
					t.Errorf("Name = %q, want %q", agent.Name, "Async Agent")
				}
				if len(agent.HTTPTools) != 1 {
					t.Fatalf("HTTPTools count = %d, want 1", len(agent.HTTPTools))
				}
				ht := agent.HTTPTools["veo"]
				if ht.AsyncPolling == nil {
					t.Fatal("AsyncPolling is nil")
				}
				ap := ht.AsyncPolling
				if !ap.Enabled {
					t.Error("Enabled should be true")
				}
				if ap.PollIntervalMS != 10000 {
					t.Errorf("PollIntervalMS = %d, want 10000", ap.PollIntervalMS)
				}
				if ap.MaxWaitMS != 300000 {
					t.Errorf("MaxWaitMS = %d, want 300000", ap.MaxWaitMS)
				}
				if ap.OperationNamePath != "name" {
					t.Errorf("OperationNamePath = %q, want %q", ap.OperationNamePath, "name")
				}
				if ap.DonePath != "done" {
					t.Errorf("DonePath = %q, want %q", ap.DonePath, "done")
				}
				if ap.ResultPath != "response.generateVideoResponse.generatedSamples[0].video.uri" {
					t.Errorf("ResultPath = %q", ap.ResultPath)
				}
				if ap.DownloadAuth != "inherit" {
					t.Errorf("DownloadAuth = %q, want %q", ap.DownloadAuth, "inherit")
				}
			},
		},
		{
			name:  "omitted download_auth defaults to empty string",
			input: "---\nname: NoAuth\ndescription: test\nhttp_tools:\n  t:\n    url: http://x\n    method: POST\n    async_polling:\n      enabled: true\n      poll_interval_ms: 5000\n      max_wait_ms: 60000\n      operation_name_path: name\n      done_path: done\n      result_path: result.uri\n---\n",
			check: func(t *testing.T, agent GuestAgent) {
				ap := agent.HTTPTools["t"].AsyncPolling
				if ap == nil {
					t.Fatal("AsyncPolling is nil")
				}
				if ap.DownloadAuth != "" {
					t.Errorf("DownloadAuth = %q, want empty string", ap.DownloadAuth)
				}
			},
		},
		{
			name:  "nil async_polling when not specified",
			input: "---\nname: Plain\ndescription: test\nhttp_tools:\n  t:\n    url: http://x\n    method: POST\n---\n",
			check: func(t *testing.T, agent GuestAgent) {
				if agent.HTTPTools["t"].AsyncPolling != nil {
					t.Error("AsyncPolling should be nil when not specified")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, err := ParseFrontmatter([]byte(tt.input))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, agent)
			}
		})
	}
}

func TestParseFrontmatter_PostProcess(t *testing.T) {
	t.Run("parses post_process with config nesting", func(t *testing.T) {
		input := `---
name: Artist
description: Makes art
http_tools:
  img-gen:
    url: http://example.com/generate
    method: POST
    request_template:
      prompt: "${input.prompt}"
    response_artifact:
      type: image/png
      save_to: "out/${input.filename}"
      post_process:
        - op: chroma_key
          config:
            tolerance: "0.30"
---

Body.`
		agent, err := ParseFrontmatter([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		ht := agent.HTTPTools["img-gen"]
		if ht.ResponseArtifact == nil {
			t.Fatal("ResponseArtifact is nil")
		}
		pp := ht.ResponseArtifact.PostProcess
		if len(pp) != 1 {
			t.Fatalf("PostProcess count = %d, want 1", len(pp))
		}
		if pp[0].Op != "chroma_key" {
			t.Errorf("Op = %q, want %q", pp[0].Op, "chroma_key")
		}
		if pp[0].Config["tolerance"] != "0.30" {
			t.Errorf("Config[tolerance] = %q, want %q", pp[0].Config["tolerance"], "0.30")
		}
	})

	t.Run("nil post_process when omitted", func(t *testing.T) {
		input := `---
name: Agent
description: test
http_tools:
  t:
    url: http://x
    method: POST
    request_template:
      q: "${input.q}"
    response_artifact:
      type: image/png
      save_to: "out/${input.filename}"
---
`
		agent, err := ParseFrontmatter([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if agent.HTTPTools["t"].ResponseArtifact.PostProcess != nil {
			t.Errorf("PostProcess should be nil when omitted, got %v",
				agent.HTTPTools["t"].ResponseArtifact.PostProcess)
		}
	})

	t.Run("multiple post_process ops", func(t *testing.T) {
		input := `---
name: Agent
description: test
http_tools:
  t:
    url: http://x
    method: POST
    request_template:
      q: "${input.q}"
    response_artifact:
      type: image/png
      save_to: "out/${input.filename}"
      post_process:
        - op: extract_video_frames
          config:
            fps: "8"
        - op: video_matte
          config:
            keyframe_every: "8"
---
`
		agent, err := ParseFrontmatter([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		pp := agent.HTTPTools["t"].ResponseArtifact.PostProcess
		if len(pp) != 2 {
			t.Fatalf("PostProcess count = %d, want 2", len(pp))
		}
		if pp[0].Config["fps"] != "8" {
			t.Errorf("first op fps = %q", pp[0].Config["fps"])
		}
		if pp[1].Config["keyframe_every"] != "8" {
			t.Errorf("second op keyframe_every = %q", pp[1].Config["keyframe_every"])
		}
	})

	t.Run("op without config", func(t *testing.T) {
		input := `---
name: Agent
description: test
http_tools:
  t:
    url: http://x
    method: POST
    request_template:
      q: "${input.q}"
    response_artifact:
      type: image/png
      save_to: "out/${input.filename}"
      post_process:
        - op: chroma_key
---
`
		agent, err := ParseFrontmatter([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		pp := agent.HTTPTools["t"].ResponseArtifact.PostProcess
		if len(pp) != 1 {
			t.Fatalf("PostProcess count = %d, want 1", len(pp))
		}
		if pp[0].Op != "chroma_key" {
			t.Errorf("Op = %q", pp[0].Op)
		}
		if len(pp[0].Config) > 0 {
			t.Errorf("Config should be nil/empty, got %v", pp[0].Config)
		}
	})
}

func TestValidatePostProcess(t *testing.T) {
	t.Run("chroma_key is a known op", func(t *testing.T) {
		errs := ValidatePostProcess([]PostProcessOp{
			{Op: "chroma_key", Config: map[string]string{"tolerance": "0.30"}},
		})
		if len(errs) != 0 {
			t.Errorf("expected no errors, got %v", errs)
		}
	})

	t.Run("all video pipeline ops are known", func(t *testing.T) {
		errs := ValidatePostProcess([]PostProcessOp{
			{Op: "extract_video_frames", Config: map[string]string{"fps": "8"}},
			{Op: "video_matte", Config: map[string]string{"keyframe_every": "8"}},
			{Op: "normalize_frames", Config: map[string]string{"padding": "4"}},
			{Op: "stitch_spritesheet", Config: map[string]string{"columns": "4"}},
		})
		if len(errs) != 0 {
			t.Errorf("expected no errors for full pipeline, got %v", errs)
		}
	})

	t.Run("retired remove_background is no longer accepted", func(t *testing.T) {
		errs := ValidatePostProcess([]PostProcessOp{
			{Op: "remove_background"},
		})
		if len(errs) != 1 {
			t.Fatalf("expected 1 error for retired op, got %d", len(errs))
		}
		if !strings.Contains(errs[0].Error(), "unknown op") {
			t.Errorf("error = %q, want mention of 'unknown op'", errs[0])
		}
	})

	t.Run("unknown op returns error", func(t *testing.T) {
		errs := ValidatePostProcess([]PostProcessOp{
			{Op: "nonexistent_op"},
		})
		if len(errs) != 1 {
			t.Fatalf("expected 1 error, got %d", len(errs))
		}
		if !strings.Contains(errs[0].Error(), "unknown op") {
			t.Errorf("error = %q, want mention of 'unknown op'", errs[0])
		}
		if !strings.Contains(errs[0].Error(), "nonexistent_op") {
			t.Errorf("error = %q, want mention of op name", errs[0])
		}
	})

	t.Run("missing op field returns error", func(t *testing.T) {
		errs := ValidatePostProcess([]PostProcessOp{
			{Op: ""},
		})
		if len(errs) != 1 {
			t.Fatalf("expected 1 error, got %d", len(errs))
		}
		if !strings.Contains(errs[0].Error(), "missing op") {
			t.Errorf("error = %q, want mention of 'missing op'", errs[0])
		}
	})

	t.Run("nil/empty list returns no errors", func(t *testing.T) {
		if errs := ValidatePostProcess(nil); len(errs) != 0 {
			t.Errorf("nil: expected no errors, got %v", errs)
		}
		if errs := ValidatePostProcess([]PostProcessOp{}); len(errs) != 0 {
			t.Errorf("empty: expected no errors, got %v", errs)
		}
	})

	t.Run("mixed known and unknown ops", func(t *testing.T) {
		errs := ValidatePostProcess([]PostProcessOp{
			{Op: "chroma_key"},
			{Op: "bad_op"},
			{Op: ""},
		})
		if len(errs) != 2 {
			t.Fatalf("expected 2 errors, got %d: %v", len(errs), errs)
		}
	})
}

func TestParseFrontmatter_InputTypes(t *testing.T) {
	t.Parallel()
	mkYAML := func(httpToolsBlock string) string {
		return "---\nname: Test\ndescription: Test agent\nhttp_tools:\n  t:\n    url: http://x\n    method: POST\n" + httpToolsBlock + "\n---\nBody."
	}

	t.Run("valid_file_type", func(t *testing.T) {
		input := mkYAML(`    request_template:
      data: "${input.img}"
    input_types:
      img:
        type: file
        encoding: base64
        max_size_mb: 10`)
		agent, err := ParseFrontmatter([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		spec := agent.HTTPTools["t"].InputTypes["img"]
		if spec.Type != "file" || spec.Encoding != "base64" || spec.MaxSizeMB != 10 {
			t.Errorf("unexpected spec: %+v", spec)
		}
	})

	t.Run("defaults_applied", func(t *testing.T) {
		input := mkYAML(`    request_template:
      data: "${input.img}"
    input_types:
      img:
        type: file`)
		agent, err := ParseFrontmatter([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		spec := agent.HTTPTools["t"].InputTypes["img"]
		if spec.Encoding != "base64" {
			t.Errorf("encoding default: got %q, want base64", spec.Encoding)
		}
		if spec.MaxSizeMB != 10 {
			t.Errorf("max_size_mb default: got %d, want 10", spec.MaxSizeMB)
		}
	})

	t.Run("string_type_explicit", func(t *testing.T) {
		input := mkYAML(`    request_template:
      data: "${input.prompt}"
    input_types:
      prompt:
        type: string`)
		_, err := ParseFrontmatter([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unsupported_type", func(t *testing.T) {
		input := mkYAML(`    request_template:
      data: "${input.img}"
    input_types:
      img:
        type: url`)
		_, err := ParseFrontmatter([]byte(input))
		if err == nil || !strings.Contains(err.Error(), "unsupported input type") {
			t.Fatalf("expected unsupported type error, got: %v", err)
		}
	})

	t.Run("invalid_encoding", func(t *testing.T) {
		input := mkYAML(`    request_template:
      data: "${input.img}"
    input_types:
      img:
        type: file
        encoding: hex`)
		_, err := ParseFrontmatter([]byte(input))
		if err == nil || !strings.Contains(err.Error(), "encoding") {
			t.Fatalf("expected encoding error, got: %v", err)
		}
	})

	t.Run("max_size_too_large", func(t *testing.T) {
		input := mkYAML(`    request_template:
      data: "${input.img}"
    input_types:
      img:
        type: file
        max_size_mb: 100`)
		_, err := ParseFrontmatter([]byte(input))
		if err == nil || !strings.Contains(err.Error(), "max_size_mb") {
			t.Fatalf("expected max_size_mb error, got: %v", err)
		}
	})

	t.Run("max_size_negative", func(t *testing.T) {
		input := mkYAML(`    request_template:
      data: "${input.img}"
    input_types:
      img:
        type: file
        max_size_mb: -1`)
		_, err := ParseFrontmatter([]byte(input))
		if err == nil || !strings.Contains(err.Error(), "max_size_mb") {
			t.Fatalf("expected max_size_mb error, got: %v", err)
		}
	})

	t.Run("key_not_in_template_strict", func(t *testing.T) {
		input := mkYAML(`    request_template:
      data: "${input.img}"
    input_types:
      typo_img:
        type: file`)
		_, err := ParseFrontmatter([]byte(input))
		if err == nil || !strings.Contains(err.Error(), "not found in request_template") {
			t.Fatalf("expected template mismatch error, got: %v", err)
		}
	})

	t.Run("key_in_template_no_input_type", func(t *testing.T) {
		input := mkYAML(`    request_template:
      data: "${input.prompt}"`)
		_, err := ParseFrontmatter([]byte(input))
		if err != nil {
			t.Fatalf("template vars without input_types should be allowed: %v", err)
		}
	})

	t.Run("omitted", func(t *testing.T) {
		input := mkYAML(`    request_template:
      data: "${input.prompt}"`)
		agent, err := ParseFrontmatter([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if agent.HTTPTools["t"].InputTypes != nil {
			t.Error("InputTypes should be nil when omitted")
		}
	})
}

func TestParseFrontmatterMaxTurns(t *testing.T) {
	base := func(extra string) string {
		return "---\nname: MaxTurnsAgent\ndescription: Fixture for max_turns parsing\n" + extra + "---\n"
	}

	t.Run("omitted defaults to zero", func(t *testing.T) {
		agent, err := ParseFrontmatter([]byte(base("")))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if agent.MaxTurns != 0 {
			t.Errorf("MaxTurns = %d, want 0 (unset)", agent.MaxTurns)
		}
	})

	t.Run("positive value parses", func(t *testing.T) {
		agent, err := ParseFrontmatter([]byte(base("max_turns: 20\n")))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if agent.MaxTurns != 20 {
			t.Errorf("MaxTurns = %d, want 20", agent.MaxTurns)
		}
	})

	t.Run("negative value rejected", func(t *testing.T) {
		_, err := ParseFrontmatter([]byte(base("max_turns: -3\n")))
		if err == nil || !strings.Contains(err.Error(), "max_turns") {
			t.Fatalf("expected max_turns error, got: %v", err)
		}
	})

	t.Run("above hard cap dropped", func(t *testing.T) {
		agent, err := ParseFrontmatter([]byte(base("max_turns: 1000000\n")))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if agent.MaxTurns != 0 {
			t.Errorf("MaxTurns = %d, want 0 (dropped above cap)", agent.MaxTurns)
		}
	})

	t.Run("zero is valid and means unset", func(t *testing.T) {
		agent, err := ParseFrontmatter([]byte(base("max_turns: 0\n")))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if agent.MaxTurns != 0 {
			t.Errorf("MaxTurns = %d, want 0", agent.MaxTurns)
		}
	})

	t.Run("exactly at hard cap kept", func(t *testing.T) {
		input := base("max_turns: 100\n")
		agent, err := ParseFrontmatter([]byte(input))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if agent.MaxTurns != MaxTurnsHardCap {
			t.Errorf("MaxTurns = %d, want %d", agent.MaxTurns, MaxTurnsHardCap)
		}
	})
}

func TestResolveMaxTurns(t *testing.T) {
	tests := []struct {
		name       string
		agent      GuestAgent
		mcpActive  bool
		configured int
		want       int
	}{
		{
			name:       "per-agent override wins over everything",
			agent:      GuestAgent{MaxTurns: 20},
			mcpActive:  false,
			configured: 0,
			want:       20,
		},
		{
			name:       "per-agent override wins even when mcp+configured",
			agent:      GuestAgent{MaxTurns: 24},
			mcpActive:  true,
			configured: 32,
			want:       24,
		},
		{
			name:       "no override, no mcp -> 1",
			agent:      GuestAgent{},
			mcpActive:  false,
			configured: 0,
			want:       1,
		},
		{
			name:       "no override, no mcp, configured ignored -> 1",
			agent:      GuestAgent{},
			mcpActive:  false,
			configured: 32,
			want:       1,
		},
		{
			name:       "no override, mcp active, no config -> default 16",
			agent:      GuestAgent{},
			mcpActive:  true,
			configured: 0,
			want:       16,
		},
		{
			name:       "no override, mcp active, config 32 -> 32",
			agent:      GuestAgent{},
			mcpActive:  true,
			configured: 32,
			want:       32,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveMaxTurns(tt.agent, tt.mcpActive, tt.configured)
			if got != tt.want {
				t.Errorf("ResolveMaxTurns() = %d, want %d", got, tt.want)
			}
		})
	}
}
