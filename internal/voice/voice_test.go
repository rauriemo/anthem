package voice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantSections int
		wantNames    []string
	}{
		{
			name:         "empty input",
			input:        "",
			wantSections: 0,
		},
		{
			name:         "single section",
			input:        "## Identity\nName: TestBot\nStyle: Concise",
			wantSections: 1,
			wantNames:    []string{"Identity"},
		},
		{
			name:         "multiple sections",
			input:        "## Identity\nName: TestBot\n\n## Personality\nFriendly and direct\n\n## Preferences\nShort answers",
			wantSections: 3,
			wantNames:    []string{"Identity", "Personality", "Preferences"},
		},
		{
			name:         "content before first section is ignored",
			input:        "# Voice\n\nPreamble text\n\n## Identity\nName: Bot",
			wantSections: 1,
			wantNames:    []string{"Identity"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vc, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if vc.Raw != tt.input {
				t.Error("Raw should equal original input")
			}
			if len(vc.Sections) != tt.wantSections {
				t.Fatalf("len(Sections) = %d, want %d", len(vc.Sections), tt.wantSections)
			}
			for i, want := range tt.wantNames {
				if vc.Sections[i].Name != want {
					t.Errorf("Sections[%d].Name = %q, want %q", i, vc.Sections[i].Name, want)
				}
			}
		})
	}
}

func TestParseSectionContent(t *testing.T) {
	vc, err := Parse("## Identity\nName: TestBot\nStyle: Concise\n\n## Personality\nFriendly")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vc.Sections[0].Content != "Name: TestBot\nStyle: Concise" {
		t.Errorf("section content = %q, want trimmed content without trailing blank lines", vc.Sections[0].Content)
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "VOICE.md")
	os.WriteFile(path, []byte("## Identity\nBot"), 0644)

	vc, err := LoadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vc.Sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(vc.Sections))
	}
}

func TestLoadFileMissing(t *testing.T) {
	_, err := LoadFile("/nonexistent/path/VOICE.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestMerge(t *testing.T) {
	original, _ := Parse("## Identity\nOriginal identity\n\n## Personality\nOriginal personality")
	updated, _ := Parse("## Personality\nUpdated personality\n\n## Preferences\nNew section")

	merged := Merge(original, updated)

	if len(merged.Sections) != 3 {
		t.Fatalf("len(Sections) = %d, want 3", len(merged.Sections))
	}

	// Order preserved from original, then new sections appended
	if merged.Sections[0].Name != "Identity" || merged.Sections[0].Content != "Original identity" {
		t.Error("first section should be preserved Identity from original")
	}
	if merged.Sections[1].Name != "Personality" || merged.Sections[1].Content != "Updated personality" {
		t.Error("second section should be updated Personality from updated")
	}
	if merged.Sections[2].Name != "Preferences" || merged.Sections[2].Content != "New section" {
		t.Error("third section should be new Preferences appended from updated")
	}
}

func TestMergePreservesUnchanged(t *testing.T) {
	original, _ := Parse("## Identity\nBot\n\n## Style\nConcise")
	updated, _ := Parse("## Style\nVerbose")

	merged := Merge(original, updated)

	if len(merged.Sections) != 2 {
		t.Fatalf("len(Sections) = %d, want 2", len(merged.Sections))
	}
	if merged.Sections[0].Content != "Bot" {
		t.Error("unchanged section should be preserved")
	}
	if merged.Sections[1].Content != "Verbose" {
		t.Error("updated section should reflect new content")
	}
}

func TestMergeNilOriginal(t *testing.T) {
	updated, _ := Parse("## Identity\nBot")
	merged := Merge(nil, updated)
	if merged == nil {
		t.Fatal("expected non-nil result")
	}
	if len(merged.Sections) != 1 {
		t.Errorf("expected 1 section, got %d", len(merged.Sections))
	}
}

func TestMergeNilUpdated(t *testing.T) {
	original, _ := Parse("## Identity\nBot")
	merged := Merge(original, nil)
	if merged == nil {
		t.Fatal("expected non-nil result")
	}
	if len(merged.Sections) != 1 {
		t.Errorf("expected 1 section, got %d", len(merged.Sections))
	}
}

func TestMergeBothNil(t *testing.T) {
	merged := Merge(nil, nil)
	if merged == nil {
		t.Fatal("expected non-nil result")
	}
	if len(merged.Sections) != 0 {
		t.Errorf("expected 0 sections, got %d", len(merged.Sections))
	}
}

func TestMergeReconstructsRaw(t *testing.T) {
	original, _ := Parse("## Identity\nBot\n\n## Style\nConcise")
	updated, _ := Parse("## Style\nVerbose")
	merged := Merge(original, updated)

	if merged.Raw == "" {
		t.Error("merged Raw should not be empty")
	}
	if !strings.Contains(merged.Raw, "## Identity") {
		t.Error("merged Raw should contain Identity section header")
	}
	if !strings.Contains(merged.Raw, "Verbose") {
		t.Error("merged Raw should contain updated content")
	}
}

func TestAppendChangelog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "changelog.md")

	if err := AppendChangelog(path, "GH-42", "+added line\n-removed line"); err != nil {
		t.Fatalf("AppendChangelog: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading changelog: %v", err)
	}
	s := string(content)
	if !strings.Contains(s, "GH-42") {
		t.Error("changelog should contain task ID")
	}
	if !strings.Contains(s, "+added line") {
		t.Error("changelog should contain diff content")
	}
	if !strings.Contains(s, "```diff") {
		t.Error("changelog should contain diff code block")
	}
}

func TestSelfEvolutionInstruction(t *testing.T) {
	inst := SelfEvolutionInstruction()
	if inst == "" {
		t.Fatal("SelfEvolutionInstruction() should not be empty")
	}
	if strings.Contains(inst, "[CORE]") {
		t.Error("SelfEvolutionInstruction() should not reference [CORE]")
	}
}
