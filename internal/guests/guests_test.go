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
			wantCount: 4, // game-designer, code-reviewer, no-body, tooled-agent (incomplete + malformed + bad-auth-scheme skipped)
			wantSlugs: []string{"game-designer", "code-reviewer", "no-body", "tooled-agent"},
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
