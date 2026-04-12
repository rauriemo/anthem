package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/workspace"
)

func newVoiceTestOrch(t *testing.T, homeDir string) *Orchestrator {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(homeDir, ".anthem"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Tracker.Kind = "github"
	cfg.Tracker.Repo = "t/r"
	return New(Opts{
		Config:       &cfg,
		TemplateBody: "{{.issue.title}}",
		Tracker:      newNoopTracker(),
		Runner:       newNoopRunner(),
		Workspace:    workspace.NewMockWorkspaceManager(),
		EventBus:     NewMockEventBus(),
		Logger:       testLogger(),
	})
}

func TestExecuteUpdateVoice(t *testing.T) {
	home := t.TempDir()
	anthemDir := filepath.Join(home, ".anthem")

	voicePath := filepath.Join(anthemDir, "VOICE.md")
	initial := "## Identity\nName: TestBot\n\n## Tone\nFriendly\n"
	if err := os.MkdirAll(anthemDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(voicePath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	orch := newVoiceTestOrch(t, home)
	orch.homeDir = home
	orch.voiceContent = initial

	err := orch.executeUpdateVoice(context.Background(), Action{
		Type:           ActionUpdateVoice,
		SectionName:    "Tone",
		SectionContent: "Professional and concise",
	})
	if err != nil {
		t.Fatalf("executeUpdateVoice() error: %v", err)
	}

	// Verify file was updated
	data, err := os.ReadFile(voicePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "Professional and concise") {
		t.Errorf("VOICE.md should contain updated Tone section, got:\n%s", content)
	}
	if !strings.Contains(content, "Identity") {
		t.Errorf("VOICE.md should preserve Identity section, got:\n%s", content)
	}

	// Verify changelog was written
	changelogPath := filepath.Join(anthemDir, "voice-changelog.md")
	changelog, err := os.ReadFile(changelogPath)
	if err != nil {
		t.Fatalf("reading changelog: %v", err)
	}
	if !strings.Contains(string(changelog), "orchestrator") {
		t.Error("changelog should mention orchestrator as the author")
	}

	// Verify in-memory state was updated
	if !strings.Contains(orch.voiceContent, "Professional and concise") {
		t.Error("in-memory voiceContent should be updated")
	}
}

func TestExecuteUpdateVoice_NewSection(t *testing.T) {
	home := t.TempDir()
	anthemDir := filepath.Join(home, ".anthem")
	voicePath := filepath.Join(anthemDir, "VOICE.md")

	// Start with no voice file (empty VoiceConfig)
	if err := os.MkdirAll(anthemDir, 0755); err != nil {
		t.Fatal(err)
	}

	orch := newVoiceTestOrch(t, home)
	orch.homeDir = home

	err := orch.executeUpdateVoice(context.Background(), Action{
		Type:           ActionUpdateVoice,
		SectionName:    "Preferences",
		SectionContent: "Prefers bullet points",
	})
	if err != nil {
		t.Fatalf("executeUpdateVoice() error: %v", err)
	}

	data, err := os.ReadFile(voicePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "## Preferences") {
		t.Errorf("expected new Preferences section, got:\n%s", content)
	}
	if !strings.Contains(content, "Prefers bullet points") {
		t.Errorf("expected section content, got:\n%s", content)
	}
}

func TestExecuteUpdateVoice_AgentFile(t *testing.T) {
	home := t.TempDir()
	anthemDir := filepath.Join(home, ".anthem")
	if err := os.MkdirAll(anthemDir, 0755); err != nil {
		t.Fatal(err)
	}

	orch := newVoiceTestOrch(t, home)
	orch.homeDir = home

	// Create agents directory and a guest agent file
	agentsDir := filepath.Join(orch.projectRoot(), "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	guestPath := filepath.Join(agentsDir, "miyazaki.md")
	guestContent := "---\nname: Miyazaki\ndescription: Artist\nrole: 2d-artist\n---\n\n## Identity\nOriginal identity\n"
	if err := os.WriteFile(guestPath, []byte(guestContent), 0644); err != nil {
		t.Fatal(err)
	}

	err := orch.executeUpdateVoice(context.Background(), Action{
		Type:           ActionUpdateVoice,
		SectionName:    "Personality",
		SectionContent: "Creative and bold",
		AgentFile:      "miyazaki.md",
	})
	if err != nil {
		t.Fatalf("executeUpdateVoice() with AgentFile error: %v", err)
	}

	data, err := os.ReadFile(guestPath)
	if err != nil {
		t.Fatal(err)
	}
	result := string(data)
	if !strings.Contains(result, "## Personality") {
		t.Error("guest file should have Personality section")
	}
	if !strings.Contains(result, "Creative and bold") {
		t.Error("guest file should have section content")
	}
	if !strings.Contains(result, "Original identity") {
		t.Error("guest file should preserve existing sections")
	}
}

func TestExecuteUpdateVoice_EmptyAgentFileUsesDefaultRouting(t *testing.T) {
	home := t.TempDir()
	anthemDir := filepath.Join(home, ".anthem")
	voicePath := filepath.Join(anthemDir, "VOICE.md")
	if err := os.MkdirAll(anthemDir, 0755); err != nil {
		t.Fatal(err)
	}

	orch := newVoiceTestOrch(t, home)
	orch.homeDir = home

	err := orch.executeUpdateVoice(context.Background(), Action{
		Type:           ActionUpdateVoice,
		SectionName:    "Preferences",
		SectionContent: "Likes dark mode",
	})
	if err != nil {
		t.Fatalf("executeUpdateVoice() error: %v", err)
	}

	data, err := os.ReadFile(voicePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Likes dark mode") {
		t.Error("with empty AgentFile, user section should go to VOICE.md")
	}
}

func TestExecuteUpdateVoice_MergeExisting(t *testing.T) {
	home := t.TempDir()
	anthemDir := filepath.Join(home, ".anthem")
	voicePath := filepath.Join(anthemDir, "VOICE.md")

	initial := "## Identity\nName: TestBot\n\n## Tone\nCasual\n\n## Communication\nDirect\n"
	if err := os.MkdirAll(anthemDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(voicePath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	orchAgent := NewOrchestratorAgent(newNoopRunner(), "", initial, 10000, 0, 0, 0, 0, testLogger())

	orch := newVoiceTestOrch(t, home)
	orch.homeDir = home
	orch.voiceContent = initial
	orch.orchAgent = orchAgent

	err := orch.executeUpdateVoice(context.Background(), Action{
		Type:           ActionUpdateVoice,
		SectionName:    "Tone",
		SectionContent: "Formal",
	})
	if err != nil {
		t.Fatalf("executeUpdateVoice() error: %v", err)
	}

	data, err := os.ReadFile(voicePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// Updated section
	if !strings.Contains(content, "Formal") {
		t.Errorf("Tone section should be updated to Formal, got:\n%s", content)
	}
	if strings.Contains(content, "Casual") {
		t.Errorf("old Tone content 'Casual' should be replaced, got:\n%s", content)
	}

	// Preserved sections
	if !strings.Contains(content, "Identity") {
		t.Errorf("Identity section should be preserved, got:\n%s", content)
	}
	if !strings.Contains(content, "Communication") {
		t.Errorf("Communication section should be preserved, got:\n%s", content)
	}

	// OrchestratorAgent user context should be updated
	if !strings.Contains(orchAgent.userContext, "Formal") {
		t.Error("orchAgent userContext should be updated")
	}
}
