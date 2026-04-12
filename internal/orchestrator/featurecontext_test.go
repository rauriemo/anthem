package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHydrateFeatureContext(t *testing.T) {
	projectRoot := "testdata"

	tests := []struct {
		name        string
		feature     string
		wantEmpty   bool
		wantContain []string
		wantExclude []string
	}{
		{
			name:    "full feature context",
			feature: "tower-defense",
			wantContain: []string{
				"## Feature Context: tower-defense",
				"Phase: prototyping",
				"### Plan Summary",
				"tower defense level",
				"### Recent Decisions",
				"Use pixel art style",
				"Towers cost gold",
				"### Recent Activity (what changed)",
				"Created arrow tower sprite",
				"Created goblin walk cycle sprite sheet",
				"### Current State (what exists now)",
				"#### Agent Activity",
				"#### Available Artifacts",
				"goblin-walk.png",
				"arrow-tower.png",
				"2d-artist: ACTIVE",
				"3/8 sprites complete",
				"Drawing fire tower sprite",
				"game-designer: IDLE",
				"Produces: fire-tower-sprite, ice-tower-sprite",
				"Depends on: fire-tower-sprite",
				"Derived from: concept-art-enemies",
			},
			wantExclude: []string{
				"rejected-goblin.png",
				"Add flying enemies",
			},
		},
		{
			name:      "empty feature name",
			feature:   "",
			wantEmpty: true,
		},
		{
			name:      "nonexistent feature",
			feature:   "nonexistent-feature",
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := HydrateFeatureContext(projectRoot, tt.feature)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantEmpty {
				if result != "" {
					t.Errorf("expected empty result, got %q", result)
				}
				return
			}
			for _, want := range tt.wantContain {
				if !strings.Contains(result, want) {
					t.Errorf("result should contain %q\n\nFull result:\n%s", want, result)
				}
			}
			for _, exclude := range tt.wantExclude {
				if strings.Contains(result, exclude) {
					t.Errorf("result should NOT contain %q", exclude)
				}
			}
		})
	}
}

func TestHydrateFeatureContext_PartialFiles(t *testing.T) {
	dir := t.TempDir()
	featureDir := filepath.Join(dir, ".context", "features", "partial")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Only plan.md exists
	if err := os.WriteFile(filepath.Join(featureDir, "plan.md"), []byte("---\nschema_version: \"1\"\nfeature: partial\nphase: design\n---\n\nJust a plan."), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := HydrateFeatureContext(dir, "partial")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "## Feature Context: partial") {
		t.Error("should contain feature context header")
	}
	if !strings.Contains(result, "Phase: design") {
		t.Error("should contain phase")
	}
	if !strings.Contains(result, "Just a plan.") {
		t.Error("should contain plan body")
	}
	if strings.Contains(result, "### Recent Decisions") {
		t.Error("should NOT contain decisions section when file is missing")
	}
}

func TestHydrateFeatureContext_PlanWithoutFrontmatter(t *testing.T) {
	dir := t.TempDir()
	featureDir := filepath.Join(dir, ".context", "features", "nofm")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(featureDir, "plan.md"), []byte("No frontmatter, just plan text."), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := HydrateFeatureContext(dir, "nofm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "No frontmatter, just plan text.") {
		t.Error("should contain plan body even without frontmatter")
	}
}

func TestReadDecisions_FiltersToFinal(t *testing.T) {
	path := filepath.Join("testdata", ".context", "features", "tower-defense", "decisions.yaml")
	decisions, err := readDecisions(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("expected 2 final decisions, got %d", len(decisions))
	}
	for _, d := range decisions {
		if d.Status != "final" {
			t.Errorf("expected final status, got %q for %q", d.Status, d.ID)
		}
	}
}

func TestReadArtifacts_ExcludesRejected(t *testing.T) {
	path := filepath.Join("testdata", ".context", "features", "tower-defense", "artifacts.yaml")
	artifacts, err := readArtifacts(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("expected 2 non-rejected artifacts, got %d", len(artifacts))
	}
	for _, a := range artifacts {
		if a.Status == "rejected" {
			t.Errorf("rejected artifact %q should be excluded", a.ID)
		}
	}
}

func TestReadTaskState(t *testing.T) {
	path := filepath.Join("testdata", ".context", "features", "tower-defense", "task-state.yaml")
	ts, err := readTaskState(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ts.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(ts.Agents))
	}
	artist := ts.Agents["2d-artist"]
	if artist.Status != "active" {
		t.Errorf("2d-artist status = %q, want active", artist.Status)
	}
	if artist.CurrentTask != "Drawing fire tower sprite" {
		t.Errorf("2d-artist current_task = %q", artist.CurrentTask)
	}
}

func TestReadChangelog(t *testing.T) {
	path := filepath.Join("testdata", ".context", "features", "tower-defense", "changelog.yaml")
	entries, err := readChangelog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	ids := map[string]bool{entries[0].ID: true, entries[1].ID: true}
	if !ids["log-001"] || !ids["log-002"] {
		t.Errorf("expected log-001 and log-002 IDs, got %#v", entries)
	}
}

func TestReadChangelog_MissingFile(t *testing.T) {
	entries, err := readChangelog(filepath.Join(t.TempDir(), "changelog.yaml"))
	if err == nil {
		t.Fatal("expected error for missing changelog file")
	}
	if entries != nil {
		t.Errorf("expected nil entries, got %#v", entries)
	}
}

func TestReadMetadataKeys(t *testing.T) {
	types, err := ReadMetadataKeys("testdata", "tower-defense")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	spriteKeys, ok := types["sprite"]
	if !ok {
		t.Fatal("expected sprite type in metadata-keys")
	}
	if len(spriteKeys) != 4 {
		t.Fatalf("expected 4 keys for sprite type, got %d", len(spriteKeys))
	}
}

func TestReadMetadataKeys_MissingFile(t *testing.T) {
	types, err := ReadMetadataKeys(t.TempDir(), "any-feature")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if types != nil {
		t.Errorf("expected nil map for missing file, got %#v", types)
	}
}

func TestHydrateFeatureContext_NoChangelog(t *testing.T) {
	dir := t.TempDir()
	featureDir := filepath.Join(dir, ".context", "features", "nochangelog")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}
	plan := "---\nschema_version: \"1\"\nfeature: nochangelog\nphase: design\nowner: test\n---\n\nBody only, no changelog."
	if err := os.WriteFile(filepath.Join(featureDir, "plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := HydrateFeatureContext(dir, "nochangelog")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "### Recent Activity (what changed)") {
		t.Error("should not contain Recent Activity section when changelog.yaml is absent")
	}
}

func TestReadTaskState_RichFields(t *testing.T) {
	path := filepath.Join("testdata", ".context", "features", "tower-defense", "task-state.yaml")
	ts, err := readTaskState(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	artist := ts.Agents["2d-artist"]
	if artist.Progress != "3/8 sprites complete" {
		t.Errorf("progress = %q, want 3/8 sprites complete", artist.Progress)
	}
	if len(artist.Produces) != 2 || artist.Produces[0] != "fire-tower-sprite" || artist.Produces[1] != "ice-tower-sprite" {
		t.Errorf("produces = %#v, want [fire-tower-sprite ice-tower-sprite]", artist.Produces)
	}
	designer := ts.Agents["game-designer"]
	if len(designer.Dependencies) != 1 || designer.Dependencies[0] != "fire-tower-sprite" {
		t.Errorf("dependencies = %#v, want [fire-tower-sprite]", designer.Dependencies)
	}
	if ts.SchemaVersion != "2" {
		t.Errorf("schema_version = %q, want 2", ts.SchemaVersion)
	}
}
