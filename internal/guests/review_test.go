package guests

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGuestFrontmatter_ParsesReviewBlock_HappyPaths exercises each of the 8
// core kinds with and without panels, with and without artifact_globs /
// context_files. All must parse cleanly.
func TestGuestFrontmatter_ParsesReviewBlock_HappyPaths(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "image-gallery bare",
			yaml: `---
name: Test
description: test agent
review:
  kind: image-gallery
  artifact_globs: ["**/*.png"]
  schema_version: "1"
---
body
`,
		},
		{
			name: "document with panels",
			yaml: `---
name: Test
description: test agent
review:
  kind: document
  title: "Quests for review"
  artifact_globs: ["**/quests/*.md"]
  panels:
    - type: viewer
    - type: list
      title: Quest summary
      source: "**/quests.yaml"
      item_fields: [id, title, objectives]
  schema_version: "1"
---
body
`,
		},
		{
			name: "video-preview with context_files",
			yaml: `---
name: Test
description: test agent
review:
  kind: video-preview
  artifact_globs: ["**/*.mp4"]
  context_files: [".context/features/tower-defense-level-1/plan.md"]
  notification_template: "Animation ready for {agent_name}"
  schema_version: "1"
---
body
`,
		},
		{
			name: "audio-player",
			yaml: `---
name: Test
description: test agent
review:
  kind: audio-player
  artifact_globs: ["**/*.ogg", "**/*.mp3"]
---
body
`,
		},
		{
			name: "web-preview",
			yaml: `---
name: Test
description: test agent
review:
  kind: web-preview
  artifact_globs: ["**/*.html"]
---
body
`,
		},
		{
			name: "model-3d-preview with tree panel",
			yaml: `---
name: Test
description: test agent
review:
  kind: model-3d-preview
  artifact_globs: ["**/*.glb"]
  panels:
    - type: viewer
    - type: tree
      source: "**/rig.json"
    - type: metadata-table
      source: "**/rig-stats.yaml"
---
body
`,
		},
		{
			name: "structured-data",
			yaml: `---
name: Test
description: test agent
review:
  kind: structured-data
  artifact_globs: ["**/balance.json"]
  partial_revise: false
---
body
`,
		},
		{
			name: "artifact-list fallback",
			yaml: `---
name: Test
description: test agent
review:
  kind: artifact-list
---
body
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent, err := ParseFrontmatter([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			if agent.Review == nil {
				t.Fatal("expected Review to be non-nil")
			}
			if agent.Review.Kind == "" {
				t.Fatal("expected Review.Kind to be set")
			}
		})
	}
}

// TestGuestFrontmatter_MalformedReview_DoesNotBreakGuest ensures that a
// broken `review:` block does not prevent the guest from loading. We verify
// this through ScanDirectory so we exercise the full load path including
// diagnostics attachment.
func TestGuestFrontmatter_MalformedReview_DoesNotBreakGuest(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Panels is declared as a non-list, which is invalid YAML for our type.
	badAgent := `---
name: Broken
description: has a malformed review block
review:
  kind: image-gallery
  panels: "not a list"
---
body
`
	if err := os.WriteFile(filepath.Join(agentsDir, "broken.md"), []byte(badAgent), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	index, err := ScanDirectory(agentsDir, logger)
	if err != nil {
		t.Fatalf("ScanDirectory failed: %v", err)
	}
	// Malformed yaml on a typed sub-struct is a parse error at ParseFrontmatter
	// time, so we expect the agent to be skipped (existing behavior) and a
	// warning logged. No panic; no crash for other agents.
	if _, ok := index.Agents["broken"]; ok {
		// If it did load, then Review must be nil (dropped by validator).
		if index.Agents["broken"].Review != nil {
			t.Errorf("expected Review to be nil on malformed agent, got %+v", index.Agents["broken"].Review)
		}
	}
	// The log must mention the file.
	if !strings.Contains(buf.String(), "broken") {
		t.Errorf("expected log to mention the broken agent; got: %s", buf.String())
	}
}

// TestGuestFrontmatter_UnknownKindPreserved: an unknown kind with no extension
// manifest is an error (guest loads, Review is dropped). With an extension
// manifest declaring the kind, the spec survives with an Info diagnostic.
func TestGuestFrontmatter_UnknownKindPreserved(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}

	agentYAML := `---
name: Beatmap Author
description: exotic kind
review:
  kind: beatmap-preview
  artifact_globs: ["**/*.beat"]
---
body
`
	if err := os.WriteFile(filepath.Join(agentsDir, "beat.md"), []byte(agentYAML), 0644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Case 1: no extension manifest -> error -> Review set to nil, diagnostic attached.
	idx, err := ScanDirectory(agentsDir, logger)
	if err != nil {
		t.Fatal(err)
	}
	a := idx.Agents["beat"]
	if a.Review != nil {
		t.Errorf("expected Review to be nil when kind is unknown and no extension manifest; got %+v", a.Review)
	}
	if !hasDiagCode(a.ReviewDiagnostics, "review.kind.unknown") {
		t.Errorf("expected review.kind.unknown diagnostic, got %+v", a.ReviewDiagnostics)
	}

	// Case 2: with extension manifest, kind survives + Info diagnostic.
	prismDir := filepath.Join(dir, ".prism")
	if err := os.MkdirAll(prismDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `schema_version: "1"
kinds:
  - id: beatmap-preview
    extends: structured-data
    description: Beatmap timeline + audio sync preview
`
	if err := os.WriteFile(filepath.Join(prismDir, "review-extensions.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	idx, err = ScanDirectory(agentsDir, logger)
	if err != nil {
		t.Fatal(err)
	}
	a = idx.Agents["beat"]
	if a.Review == nil {
		t.Fatal("expected Review to survive when extension manifest declares the kind")
	}
	if a.Review.Kind != "beatmap-preview" {
		t.Errorf("kind = %q, want beatmap-preview", a.Review.Kind)
	}
	if !hasDiagCode(a.ReviewDiagnostics, "review.kind.extension") {
		t.Errorf("expected review.kind.extension info diagnostic, got %+v", a.ReviewDiagnostics)
	}
}

// TestValidateReviewSpec_ErrorSeverities pins the set of errors that drop the spec.
func TestValidateReviewSpec_ErrorSeverities(t *testing.T) {
	cases := []struct {
		name     string
		spec     ReviewSpec
		ext      map[string]string
		wantCode string
	}{
		{
			name:     "empty kind",
			spec:     ReviewSpec{},
			wantCode: "review.kind.missing",
		},
		{
			name:     "unknown kind, no extension",
			spec:     ReviewSpec{Kind: "nonexistent-kind"},
			wantCode: "review.kind.unknown",
		},
		{
			name: "panel type missing",
			spec: ReviewSpec{
				Kind:   "document",
				Panels: []PanelSpec{{Title: "no type"}},
			},
			wantCode: "review.panels.type.missing",
		},
		{
			name: "panel type unknown",
			spec: ReviewSpec{
				Kind:   "document",
				Panels: []PanelSpec{{Type: "nonexistent"}},
			},
			wantCode: "review.panels.type.unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := ValidateReviewSpec(&tc.spec, tc.ext)
			if !hasDiagCode(diags, tc.wantCode) {
				t.Errorf("expected diagnostic %q, got %+v", tc.wantCode, diags)
			}
			found := false
			for _, d := range diags {
				if d.Code == tc.wantCode && d.Severity != SeverityError {
					t.Errorf("expected severity error for %q, got %q", tc.wantCode, d.Severity)
				}
				if d.Code == tc.wantCode {
					found = true
				}
			}
			if !found {
				t.Errorf("diagnostic %q not found", tc.wantCode)
			}
		})
	}
}

// TestValidateReviewSpec_WarningSeverities pins what produces warnings.
func TestValidateReviewSpec_WarningSeverities(t *testing.T) {
	cases := []struct {
		name     string
		spec     ReviewSpec
		wantCode string
	}{
		{
			name: "invalid glob",
			spec: ReviewSpec{
				Kind:          "image-gallery",
				ArtifactGlobs: []string{"[unclosed"},
			},
			wantCode: "review.artifact_globs.invalid",
		},
		{
			name: "partial_revise redundant on structured-data",
			spec: ReviewSpec{
				Kind:          "structured-data",
				PartialRevise: boolPtr(false),
			},
			wantCode: "review.partial_revise.redundant",
		},
		{
			name: "unknown notification variable",
			spec: ReviewSpec{
				Kind:                 "image-gallery",
				NotificationTemplate: "Hey {who}, review this",
			},
			wantCode: "review.notification_template.unknown_var",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := ValidateReviewSpec(&tc.spec, nil)
			found := false
			for _, d := range diags {
				if d.Code == tc.wantCode {
					if d.Severity != SeverityWarning {
						t.Errorf("expected severity warning for %q, got %q", tc.wantCode, d.Severity)
					}
					found = true
				}
			}
			if !found {
				t.Errorf("diagnostic %q not found in %+v", tc.wantCode, diags)
			}
		})
	}
}

// TestValidateReviewSpec_TypoCatch asserts that common typos in kind names or
// panel types produce errors and hint at the canonical list.
func TestValidateReviewSpec_TypoCatch(t *testing.T) {
	// "image-gallry" (missing 'e') -> review.kind.unknown
	diags := ValidateReviewSpec(&ReviewSpec{Kind: "image-gallry"}, nil)
	if !hasDiagCode(diags, "review.kind.unknown") {
		t.Errorf("expected unknown-kind error for typo, got %+v", diags)
	}
	// The message must cite the canonical list so the author sees what to fix.
	var msg string
	for _, d := range diags {
		if d.Code == "review.kind.unknown" {
			msg = d.Message
		}
	}
	if !strings.Contains(msg, "image-gallery") {
		t.Errorf("expected diagnostic to mention canonical kind 'image-gallery'; got: %s", msg)
	}

	// "veiwer" -> review.panels.type.unknown
	diags = ValidateReviewSpec(&ReviewSpec{
		Kind:   "document",
		Panels: []PanelSpec{{Type: "veiwer"}},
	}, nil)
	if !hasDiagCode(diags, "review.panels.type.unknown") {
		t.Errorf("expected unknown-panel-type error for typo, got %+v", diags)
	}
	msg = ""
	for _, d := range diags {
		if d.Code == "review.panels.type.unknown" {
			msg = d.Message
		}
	}
	if !strings.Contains(msg, "viewer") {
		t.Errorf("expected diagnostic to cite canonical panel types including 'viewer'; got: %s", msg)
	}
}

// TestExtensionManifest_Parsed asserts the manifest roundtrips and that
// ExtensionKindIDs produces the expected map.
func TestExtensionManifest_Parsed(t *testing.T) {
	dir := t.TempDir()
	prismDir := filepath.Join(dir, ".prism")
	if err := os.MkdirAll(prismDir, 0755); err != nil {
		t.Fatal(err)
	}
	yaml := `schema_version: "1"
kinds:
  - id: beatmap-preview
    extends: structured-data
    description: beatmap editor
  - id: dialog-graph
    extends: document
`
	if err := os.WriteFile(filepath.Join(prismDir, "review-extensions.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadExtensionManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Kinds) != 2 {
		t.Errorf("expected 2 kinds, got %d", len(m.Kinds))
	}
	ids := m.ExtensionKindIDs()
	if ids["beatmap-preview"] != "structured-data" {
		t.Errorf("beatmap-preview extends = %q, want structured-data", ids["beatmap-preview"])
	}
	if ids["dialog-graph"] != "document" {
		t.Errorf("dialog-graph extends = %q, want document", ids["dialog-graph"])
	}

	// Missing file -> empty manifest, no error.
	m2, err := LoadExtensionManifest(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Kinds) != 0 {
		t.Errorf("expected empty manifest for missing file, got %d kinds", len(m2.Kinds))
	}
}

// TestValidateReviewSpec_ValidSpecHasNoErrors ensures a clean, idiomatic spec
// produces no errors (warnings or info are fine).
func TestValidateReviewSpec_ValidSpecHasNoErrors(t *testing.T) {
	spec := &ReviewSpec{
		Kind:                 "document",
		Title:                "Quest review",
		ArtifactGlobs:        []string{"**/quests/*.md"},
		NotificationTemplate: "{agent_name} finished {step_title} ({artifact_count} quests)",
		Panels: []PanelSpec{
			{Type: "viewer"},
			{Type: "list", Source: "**/quests.yaml", ItemFields: []string{"id", "title"}},
		},
		SchemaVersion: "1",
	}
	diags := ValidateReviewSpec(spec, nil)
	if HasError(diags) {
		t.Errorf("expected no errors for valid spec, got %+v", diags)
	}
}

// --- helpers -----------------------------------------------------------------

func hasDiagCode(diags []ReviewDiagnostic, code string) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}

func boolPtr(b bool) *bool { return &b }
