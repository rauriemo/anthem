package voice

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testMigrateLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestMigrate_HappyPath(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	voicePath := filepath.Join(dir, "VOICE.md")

	voiceContent := `## Identity
Name: Noah
Role: Senior engineer

## Personality
- Direct and opinionated

## Communication Style
- Prefers visual explanations

## Preferences
- Likes dark mode
`
	os.WriteFile(voicePath, []byte(voiceContent), 0644)

	err := MigrateVoiceToOrchestrator(voicePath, agentsDir, "Test Project", testMigrateLogger())
	if err != nil {
		t.Fatal(err)
	}

	orchPath := filepath.Join(agentsDir, "orchestrator.md")
	orchData, err := os.ReadFile(orchPath)
	if err != nil {
		t.Fatal("orchestrator.md should have been created")
	}
	orchStr := string(orchData)

	if !strings.Contains(orchStr, "name: Test Project") {
		t.Error("orchestrator.md should have project name in frontmatter")
	}
	if !strings.Contains(orchStr, "## Identity") {
		t.Error("orchestrator.md should contain Identity section")
	}
	if !strings.Contains(orchStr, "## Personality") {
		t.Error("orchestrator.md should contain Personality section")
	}
	if strings.Contains(orchStr, "Communication Style") {
		t.Error("orchestrator.md should NOT contain user sections")
	}

	voiceData, err := os.ReadFile(voicePath)
	if err != nil {
		t.Fatal("VOICE.md should still exist")
	}
	voiceStr := string(voiceData)

	if strings.Contains(voiceStr, "## Identity") {
		t.Error("VOICE.md should NOT contain Identity after migration")
	}
	if strings.Contains(voiceStr, "## Personality") {
		t.Error("VOICE.md should NOT contain Personality after migration")
	}
	if !strings.Contains(voiceStr, "## Communication Style") {
		t.Error("VOICE.md should retain Communication Style")
	}
	if !strings.Contains(voiceStr, "## Preferences") {
		t.Error("VOICE.md should retain Preferences")
	}
}

func TestMigrate_AlreadyMigrated(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	os.MkdirAll(agentsDir, 0755)
	voicePath := filepath.Join(dir, "VOICE.md")

	orchContent := "---\nname: Existing\ndescription: Already here\nrole: orchestrator\n---\n\n## Identity\nExisting agent\n"
	os.WriteFile(filepath.Join(agentsDir, "orchestrator.md"), []byte(orchContent), 0644)

	voiceContent := "## Communication Style\n- Concise\n"
	os.WriteFile(voicePath, []byte(voiceContent), 0644)

	err := MigrateVoiceToOrchestrator(voicePath, agentsDir, "Test", testMigrateLogger())
	if err != nil {
		t.Fatal(err)
	}

	orchData, _ := os.ReadFile(filepath.Join(agentsDir, "orchestrator.md"))
	if !strings.Contains(string(orchData), "Existing agent") {
		t.Error("orchestrator.md should be untouched")
	}

	voiceData, _ := os.ReadFile(voicePath)
	if !strings.Contains(string(voiceData), "Concise") {
		t.Error("VOICE.md should be untouched")
	}
}

func TestMigrate_ConflictStalePersonality(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	os.MkdirAll(agentsDir, 0755)
	voicePath := filepath.Join(dir, "VOICE.md")

	orchContent := "---\nname: Project\ndescription: Orch\nrole: orchestrator\n---\n\n## Identity\nCanonical identity\n"
	os.WriteFile(filepath.Join(agentsDir, "orchestrator.md"), []byte(orchContent), 0644)

	// VOICE.md has stale personality sections
	voiceContent := "## Identity\nStale identity\n\n## Communication Style\n- Direct\n"
	os.WriteFile(voicePath, []byte(voiceContent), 0644)

	err := MigrateVoiceToOrchestrator(voicePath, agentsDir, "Test", testMigrateLogger())
	if err != nil {
		t.Fatal(err)
	}

	// orchestrator.md should NOT be modified
	orchData, _ := os.ReadFile(filepath.Join(agentsDir, "orchestrator.md"))
	if !strings.Contains(string(orchData), "Canonical identity") {
		t.Error("orchestrator.md should be untouched when it already exists")
	}

	// VOICE.md should NOT be pruned
	voiceData, _ := os.ReadFile(voicePath)
	if !strings.Contains(string(voiceData), "Stale identity") {
		t.Error("VOICE.md stale sections should NOT be auto-pruned")
	}
}

func TestMigrate_OnlyUserSections(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	voicePath := filepath.Join(dir, "VOICE.md")

	voiceContent := "## Communication Style\n- Direct\n\n## Preferences\n- Dark mode\n"
	os.WriteFile(voicePath, []byte(voiceContent), 0644)

	err := MigrateVoiceToOrchestrator(voicePath, agentsDir, "Test", testMigrateLogger())
	if err != nil {
		t.Fatal(err)
	}

	orchPath := filepath.Join(agentsDir, "orchestrator.md")
	if _, err := os.Stat(orchPath); err == nil {
		t.Error("orchestrator.md should NOT be created when no agent sections exist")
	}
}

func TestMigrate_VoiceMDMissing(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	voicePath := filepath.Join(dir, "nonexistent", "VOICE.md")

	err := MigrateVoiceToOrchestrator(voicePath, agentsDir, "Test", testMigrateLogger())
	if err != nil {
		t.Fatalf("should not error when VOICE.md missing, got: %v", err)
	}
}

func TestMigrate_AgentsDirCreated(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "deep", "nested", "agents")
	voicePath := filepath.Join(dir, "VOICE.md")

	voiceContent := "## Identity\nI am an agent\n"
	os.WriteFile(voicePath, []byte(voiceContent), 0644)

	err := MigrateVoiceToOrchestrator(voicePath, agentsDir, "Test", testMigrateLogger())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(agentsDir, "orchestrator.md")); err != nil {
		t.Error("agents directory should have been created recursively")
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	voicePath := filepath.Join(dir, "VOICE.md")

	voiceContent := "## Identity\nAgent identity\n\n## Communication Style\n- Brief\n"
	os.WriteFile(voicePath, []byte(voiceContent), 0644)

	// First run
	err := MigrateVoiceToOrchestrator(voicePath, agentsDir, "Test", testMigrateLogger())
	if err != nil {
		t.Fatal(err)
	}

	orchData1, _ := os.ReadFile(filepath.Join(agentsDir, "orchestrator.md"))
	voiceData1, _ := os.ReadFile(voicePath)

	// Second run
	err = MigrateVoiceToOrchestrator(voicePath, agentsDir, "Test", testMigrateLogger())
	if err != nil {
		t.Fatal(err)
	}

	orchData2, _ := os.ReadFile(filepath.Join(agentsDir, "orchestrator.md"))
	voiceData2, _ := os.ReadFile(voicePath)

	if string(orchData1) != string(orchData2) {
		t.Error("second migration should not change orchestrator.md")
	}
	if string(voiceData1) != string(voiceData2) {
		t.Error("second migration should not change VOICE.md")
	}
}
