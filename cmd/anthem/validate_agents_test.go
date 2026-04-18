package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateAgentsCLI_NonZeroOnError ensures the CLI returns an error
// (which cobra turns into a non-zero exit code) when any agent has an
// error-level diagnostic.
func TestValidateAgentsCLI_NonZeroOnError(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Bad agent: unknown kind, no extension manifest.
	bad := `---
name: Bad
description: broken review spec
review:
  kind: nonexistent-kind
---
body
`
	if err := os.WriteFile(filepath.Join(agentsDir, "bad.md"), []byte(bad), 0644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	err := runValidateAgents(&out, &errOut, agentsDir, false)
	if err == nil {
		t.Fatal("expected non-nil error for agents with review spec errors")
	}
	combined := out.String() + errOut.String()
	if !strings.Contains(combined, "bad") {
		t.Errorf("expected output to mention the bad agent slug; got: %s", combined)
	}
	if !strings.Contains(out.String(), "errors") {
		t.Errorf("expected summary line to mention 'errors'; got: %s", out.String())
	}
}

// TestValidateAgentsCLI_ZeroOnClean asserts the CLI returns nil (exit 0)
// when all agents validate cleanly.
func TestValidateAgentsCLI_ZeroOnClean(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	good := `---
name: Good
description: clean review spec
review:
  kind: document
  artifact_globs: ["**/*.md"]
  schema_version: "1"
---
body
`
	if err := os.WriteFile(filepath.Join(agentsDir, "good.md"), []byte(good), 0644); err != nil {
		t.Fatal(err)
	}
	// An agent with no review block is also fine.
	noReview := `---
name: No Review
description: no review block is fine
---
body
`
	if err := os.WriteFile(filepath.Join(agentsDir, "noreview.md"), []byte(noReview), 0644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	err := runValidateAgents(&out, &errOut, agentsDir, false)
	if err != nil {
		t.Fatalf("expected nil error for clean agents; got %v", err)
	}
	if !strings.Contains(out.String(), "good: ok") {
		t.Errorf("expected 'good: ok' in output; got: %s", out.String())
	}
}

// TestValidateAgentsCLI_QuietSuppressesDetail: --quiet hides per-diagnostic
// output but still prints the summary.
func TestValidateAgentsCLI_QuietSuppressesDetail(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	good := `---
name: Good
description: clean
review:
  kind: document
---
body
`
	if err := os.WriteFile(filepath.Join(agentsDir, "good.md"), []byte(good), 0644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := runValidateAgents(&out, &errOut, agentsDir, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out.String(), "good: ok") {
		t.Errorf("quiet mode should not print per-agent ok lines; got: %s", out.String())
	}
	if !strings.Contains(out.String(), "Validated") {
		t.Errorf("summary line should still be printed; got: %s", out.String())
	}
}

// TestValidateAgentsCLI_ExtensionManifest: extension kinds are not flagged as unknown.
func TestValidateAgentsCLI_ExtensionManifest(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	prismDir := filepath.Join(dir, ".prism")
	if err := os.MkdirAll(prismDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `schema_version: "1"
kinds:
  - id: beatmap-preview
    extends: structured-data
`
	if err := os.WriteFile(filepath.Join(prismDir, "review-extensions.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	agent := `---
name: Beatmap
description: custom kind
review:
  kind: beatmap-preview
---
body
`
	if err := os.WriteFile(filepath.Join(agentsDir, "beatmap.md"), []byte(agent), 0644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := runValidateAgents(&out, &errOut, agentsDir, false); err != nil {
		t.Fatalf("expected nil error for extension-declared kind; got %v", err)
	}
}

// TestValidateAgentsCLI_MissingDir fails with a clear error when the dir
// doesn't exist. This is important for CI where a typo should produce a
// readable error, not a silent pass.
func TestValidateAgentsCLI_MissingDir(t *testing.T) {
	var out, errOut bytes.Buffer
	err := runValidateAgents(&out, &errOut, filepath.Join(t.TempDir(), "does-not-exist"), false)
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
}
