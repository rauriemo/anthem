package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rauriemo/anthem/internal/guests"
	"github.com/rauriemo/anthem/internal/prompt"
	"github.com/rauriemo/anthem/internal/types"
)

// TestParity_StoryContextRendersInExecuteMode verifies that the
// StoryContext block an orchestrator builds for a chat turn survives
// the chat/execute dispatch split introduced by plan decision D10.
// renderStoryContextText is the single-source formatter both paths
// now flow through; a regression here would mean Tolkien (and any
// other agent that writes to story/) can no longer see the global
// story bible after the prompt extraction.
func TestParity_StoryContextRendersInExecuteMode(t *testing.T) {
	sc := &StoryContext{
		Config: "tone: epic\n",
		Files: map[string]string{
			"world.md": "# World\n<!-- id: geography -->\n## Geography\nMountains to the north.",
		},
	}

	text := renderStoryContextText(sc)
	if !strings.Contains(text, "## Story Bible") {
		t.Fatalf("story context text missing Story Bible heading: %q", text)
	}
	if !strings.Contains(text, "world.md") {
		t.Fatalf("story context text missing world.md file: %q", text)
	}

	assembled := prompt.BuildGuestPrompt(
		"persona body",
		prompt.LiveContext{},
		"please continue the story",
		prompt.GuestPromptOpts{
			Mode:             types.ModeExecute,
			StoryContextText: text,
		},
	)
	if !strings.Contains(assembled, "## Story Bible") {
		t.Fatalf("execute-mode prompt dropped story context: %q", assembled)
	}
	if !strings.Contains(assembled, "please continue the story") {
		t.Fatalf("execute-mode prompt missing user message: %q", assembled)
	}
}

// TestParity_LoadPersonaErrors verifies that guests.LoadPersona
// returns a descriptive error for a missing persona file and that
// the error path the execute runner relies on (silent fallback to
// empty persona body) does not panic. Buried inside buildStepPrompt
// this only fires when agents_dir exists but the agent.md file
// doesn't -- a common symptom of a renamed or unregistered guest.
func TestParity_LoadPersonaErrors(t *testing.T) {
	dir := t.TempDir()

	_, err := guests.LoadPersona(dir, "ghost-agent")
	if err == nil {
		t.Fatalf("expected error for missing persona, got nil")
	}
	if !strings.Contains(err.Error(), "ghost-agent") {
		t.Fatalf("error should name the agent slug, got: %v", err)
	}

	personaPath := filepath.Join(dir, "miyazaki.md")
	if err := os.WriteFile(personaPath, []byte("---\nid: miyazaki\n---\n\nI am Miyazaki."), 0o644); err != nil {
		t.Fatalf("write persona: %v", err)
	}
	body, err := guests.LoadPersona(dir, "miyazaki")
	if err != nil {
		t.Fatalf("unexpected error loading valid persona: %v", err)
	}
	if !strings.Contains(body, "I am Miyazaki") {
		t.Fatalf("persona body missing expected content: %q", body)
	}
}

// TestParity_ContextReportPropagatesThroughFeatureWriter verifies the
// end-to-end path a guest agent relies on to register an artifact:
// ParseContextReport extracts the JSON block from the guest's output,
// AppendArtifact stamps origin and writes artifacts.yaml, and
// readArtifacts surfaces the entry back through HydrateFeatureContext
// with its structured origin intact. A regression in any link of this
// chain means chained tasks stop seeing what upstream steps produced.
func TestParity_ContextReportPropagatesThroughFeatureWriter(t *testing.T) {
	root := t.TempDir()
	feature := "character-sprites"
	agentOutput := "All done!\n\n" +
		`{"context_report":{"action":"asset_created","summary":"Hero sprite v1",` +
		`"artifact":{"id":"hero-sprite-v1","type":"image/png","path":"assets/hero.png",` +
		`"description":"Hero idle pose","origin":{"kind":"chat","agent_id":"forged"},` +
		`"agent_id":"also-forged"}}}`

	report, err := ParseContextReport(agentOutput)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if report == nil || report.Artifact == nil {
		t.Fatalf("expected parsed artifact, got %+v", report)
	}

	entry := ArtifactEntry{
		ID:          report.Artifact.ID,
		Type:        report.Artifact.Type,
		Path:        report.Artifact.Path,
		Description: report.Artifact.Description,
		Feature:     feature,
		CreatedBy:   "miyazaki",
		CreatedAt:   "2026-04-20T10:00:00Z",
		Status:      "draft",
	}
	if err := AppendArtifact(root, feature, entry, ChatOrigin{AgentID: "miyazaki"}); err != nil {
		t.Fatalf("append artifact: %v", err)
	}

	ctxText, err := HydrateFeatureContext(root, feature)
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if !strings.Contains(ctxText, "hero-sprite-v1") {
		t.Fatalf("hydrated context missing artifact id: %q", ctxText)
	}
	if !strings.Contains(ctxText, "assets/hero.png") {
		t.Fatalf("hydrated context missing path: %q", ctxText)
	}

	artifacts, err := readArtifacts(filepath.Join(root, ".context", "features", feature, "artifacts.yaml"))
	if err != nil {
		t.Fatalf("read artifacts: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	got := artifacts[0]
	if got.Origin == nil {
		t.Fatalf("origin was not stamped by writer")
	}
	if got.Origin.Kind != OriginKindChat {
		t.Fatalf("origin kind should be chat (writer-stamped), got %q", got.Origin.Kind)
	}
	if got.Origin.AgentID != "miyazaki" {
		t.Fatalf("origin agent should come from call-site tag (miyazaki), got %q -- agent-supplied origin leaked through", got.Origin.AgentID)
	}
}
