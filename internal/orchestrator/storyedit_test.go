package orchestrator

import (
	"strings"
	"testing"
)

func TestExtractStoryEdits_SingleBlockAllAttrs(t *testing.T) {
	response := "I've refined chapter 1.\n\n```story-edit target=\"narrative.md\" section=\"chapter_01_first_wave\" rev=\"a1b2c3d4\"\n## Chapter 1: The First Wave\n\nRevised content here.\n```\n\nLet me know."
	explanation, edits, hasEdits := extractStoryEdits(response)

	if !hasEdits {
		t.Fatal("expected hasEdits to be true")
	}
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	e := edits[0]
	if e.Target != "narrative.md" {
		t.Errorf("target = %q, want narrative.md", e.Target)
	}
	if e.Section != "chapter_01_first_wave" {
		t.Errorf("section = %q, want chapter_01_first_wave", e.Section)
	}
	if e.Rev != "a1b2c3d4" {
		t.Errorf("rev = %q, want a1b2c3d4", e.Rev)
	}
	if !strings.Contains(e.Content, "Revised content here.") {
		t.Errorf("content missing expected text: %q", e.Content)
	}
	if strings.Contains(explanation, "story-edit") {
		t.Error("explanation should not contain story-edit block")
	}
	if !strings.Contains(explanation, "refined chapter 1") {
		t.Error("explanation should contain surrounding text")
	}
}

func TestExtractStoryEdits_AfterAttribute(t *testing.T) {
	response := "```story-edit target=\"narrative.md\" after=\"chapter_01_first_wave\"\n<!-- id: interlude -->\n## Interlude\n\nNew content.\n```"
	_, edits, hasEdits := extractStoryEdits(response)

	if !hasEdits {
		t.Fatal("expected hasEdits")
	}
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if edits[0].After != "chapter_01_first_wave" {
		t.Errorf("after = %q", edits[0].After)
	}
	if edits[0].Section != "" {
		t.Errorf("section should be empty for insert, got %q", edits[0].Section)
	}
}

func TestExtractStoryEdits_DefaultTarget(t *testing.T) {
	response := "```story-edit\n<!-- id: epilogue -->\n## Epilogue\n\nThe towers still hum.\n```"
	_, edits, hasEdits := extractStoryEdits(response)

	if !hasEdits {
		t.Fatal("expected hasEdits")
	}
	if edits[0].Target != "narrative.md" {
		t.Errorf("default target = %q, want narrative.md", edits[0].Target)
	}
}

func TestExtractStoryEdits_MultipleBlocks(t *testing.T) {
	response := "Updating two files.\n\n```story-edit target=\"narrative.md\" section=\"prologue\" rev=\"aaaa\"\nNew prologue.\n```\n\nAnd also:\n\n```story-edit target=\"world.md\" section=\"geography_overview\" rev=\"bbbb\"\nNew geography.\n```"
	explanation, edits, hasEdits := extractStoryEdits(response)

	if !hasEdits {
		t.Fatal("expected hasEdits")
	}
	if len(edits) != 2 {
		t.Fatalf("expected 2 edits, got %d", len(edits))
	}
	if edits[0].Target != "narrative.md" {
		t.Errorf("first target = %q", edits[0].Target)
	}
	if edits[1].Target != "world.md" {
		t.Errorf("second target = %q", edits[1].Target)
	}
	if !strings.Contains(explanation, "Updating two files") {
		t.Error("explanation should contain chat text")
	}
}

func TestExtractStoryEdits_NoBlocks(t *testing.T) {
	response := "I think the story is solid. No changes needed."
	explanation, edits, hasEdits := extractStoryEdits(response)

	if hasEdits {
		t.Error("expected no edits")
	}
	if len(edits) != 0 {
		t.Errorf("expected 0 edits, got %d", len(edits))
	}
	if explanation != response {
		t.Errorf("explanation should be original response")
	}
}

func TestExtractStoryEdits_MalformedBlock(t *testing.T) {
	response := "```story-edit target=\"narrative.md\"\nContent without closing fence"
	_, _, hasEdits := extractStoryEdits(response)

	if hasEdits {
		t.Error("malformed block should not produce edits")
	}
}

func TestExtractStoryEdits_NestedCodeBlocks(t *testing.T) {
	response := "```story-edit target=\"narrative.md\" section=\"prologue\" rev=\"abcd\"\n## Prologue\n\nSome code example:\n\n    func main() {}\n\n```"
	_, edits, hasEdits := extractStoryEdits(response)

	if !hasEdits {
		t.Fatal("expected hasEdits")
	}
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if !strings.Contains(edits[0].Content, "func main()") {
		t.Error("content should include nested code")
	}
}

func TestExtractStoryEdits_ExplanationStripped(t *testing.T) {
	response := "Before text.\n\n```story-edit target=\"narrative.md\" section=\"prologue\" rev=\"abcd\"\nNew content.\n```\n\nAfter text."
	explanation, _, _ := extractStoryEdits(response)

	if strings.Contains(explanation, "```") {
		t.Error("explanation should have story-edit blocks stripped")
	}
	if !strings.Contains(explanation, "Before text.") || !strings.Contains(explanation, "After text.") {
		t.Error("explanation should contain surrounding text")
	}
}
