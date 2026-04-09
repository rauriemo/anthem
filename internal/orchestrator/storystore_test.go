package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupStoryDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	storyDir := filepath.Join(dir, "story")
	if err := os.MkdirAll(storyDir, 0755); err != nil {
		t.Fatal(err)
	}

	contextYAML := `game: TestGame
genre: fantasy
`
	if err := os.WriteFile(filepath.Join(storyDir, "context.yaml"), []byte(contextYAML), 0644); err != nil {
		t.Fatal(err)
	}

	narrative := `---
title: Test Story
status: draft
sections:
  prologue:
    status: draft
    edited_by: human
    edited_at: "2026-01-01T00:00:00Z"
  chapter_01:
    status: draft
    edited_by: human
    edited_at: "2026-01-01T00:00:00Z"
---

<!-- id: prologue -->
## Prologue

The story begins here.

<!-- id: chapter_01 -->
## Chapter 1

The first chapter content.
`
	if err := os.WriteFile(filepath.Join(storyDir, "narrative.md"), []byte(narrative), 0644); err != nil {
		t.Fatal(err)
	}

	characters := `---
title: Characters
status: draft
sections:
  hero:
    status: draft
    edited_by: human
    edited_at: "2026-01-01T00:00:00Z"
---

<!-- id: hero -->
## The Hero

A brave warrior.
`
	if err := os.WriteFile(filepath.Join(storyDir, "characters.md"), []byte(characters), 0644); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestParseSections(t *testing.T) {
	content := `---
title: Test
---

<!-- id: sec_a -->
## Section A

Content A.

<!-- id: sec_b -->
## Section B

Content B.
`
	fm, sections := parseSections(content)
	if fm == "" {
		t.Error("expected non-empty frontmatter")
	}
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(sections))
	}
	if sections[0].ID != "sec_a" {
		t.Errorf("first section ID = %q", sections[0].ID)
	}
	if sections[1].ID != "sec_b" {
		t.Errorf("second section ID = %q", sections[1].ID)
	}
	if sections[0].Heading != "## Section A" {
		t.Errorf("heading = %q", sections[0].Heading)
	}
}

func TestParseSections_NoSections(t *testing.T) {
	content := `---
title: Empty
---

No sections here.
`
	_, sections := parseSections(content)
	if len(sections) != 0 {
		t.Errorf("expected 0 sections, got %d", len(sections))
	}
}

func TestParseSections_NoHeading(t *testing.T) {
	content := `---
title: Test
---

<!-- id: bare -->
Just content, no heading.
`
	_, sections := parseSections(content)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}
	if sections[0].Heading != "" {
		t.Errorf("expected empty heading, got %q", sections[0].Heading)
	}
}

func TestParseSections_LastExtendsToEOF(t *testing.T) {
	content := `---
title: Test
---

<!-- id: only -->
## Only Section

Lots of content here.
More lines.
Even more.
`
	_, sections := parseSections(content)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}
	if !strings.Contains(sections[0].Content, "Even more.") {
		t.Error("last section should extend to EOF")
	}
}

func TestComputeHash(t *testing.T) {
	h1 := computeHash("hello world")
	h2 := computeHash("hello world")
	h3 := computeHash("different")

	if h1 != h2 {
		t.Error("same content should produce same hash")
	}
	if h1 == h3 {
		t.Error("different content should produce different hash")
	}
	if len(h1) != 8 {
		t.Errorf("hash should be 8 chars, got %d", len(h1))
	}
}

func TestStoryStore_CheckRevision_Match(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	ctx, err := store.ReadContext()
	if err != nil {
		t.Fatal(err)
	}

	prologueHash := ctx.Sections["narrative.md"]["prologue"]

	edit := StoryEdit{Target: "narrative.md", Section: "prologue", Rev: prologueHash}
	if err := store.CheckRevision(edit); err != nil {
		t.Errorf("matching rev should pass: %v", err)
	}
}

func TestStoryStore_CheckRevision_Mismatch(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	edit := StoryEdit{Target: "narrative.md", Section: "prologue", Rev: "stale123"}
	if err := store.CheckRevision(edit); err != ErrStaleRevision {
		t.Errorf("expected ErrStaleRevision, got %v", err)
	}
}

func TestStoryStore_CheckRevision_EmptyRev(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	edit := StoryEdit{Target: "narrative.md", Section: "prologue", Rev: ""}
	if err := store.CheckRevision(edit); err != nil {
		t.Errorf("empty rev should pass: %v", err)
	}
}

func TestStoryStore_CheckRevision_SectionNotFound(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	edit := StoryEdit{Target: "narrative.md", Section: "nonexistent", Rev: "abc"}
	if err := store.CheckRevision(edit); err != ErrSectionIDNotFound {
		t.Errorf("expected ErrSectionIDNotFound, got %v", err)
	}
}

func TestStoryStore_Apply_Replace(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	edit := StoryEdit{
		Target:  "narrative.md",
		Section: "prologue",
		Content: "<!-- id: prologue -->\n## Prologue\n\nCompletely new prologue content.",
	}
	if err := store.Apply(edit, "story-writer"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "story", "narrative.md"))
	content := string(data)
	if !strings.Contains(content, "Completely new prologue content.") {
		t.Error("replaced content should be present")
	}
	if !strings.Contains(content, "The first chapter content.") {
		t.Error("other sections should be unchanged")
	}
}

func TestStoryStore_Apply_InsertAfter(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	edit := StoryEdit{
		Target:  "narrative.md",
		After:   "prologue",
		Content: "<!-- id: interlude -->\n## Interlude\n\nBetween prologue and chapter 1.",
	}
	if err := store.Apply(edit, "story-writer"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "story", "narrative.md"))
	content := string(data)
	prologueAnchor := strings.Index(content, "<!-- id: prologue -->")
	interludeAnchor := strings.Index(content, "<!-- id: interlude -->")
	chapter01Anchor := strings.Index(content, "<!-- id: chapter_01 -->")

	if prologueAnchor < 0 || interludeAnchor < 0 || chapter01Anchor < 0 {
		t.Fatalf("missing anchors in output: prologue=%d interlude=%d chapter_01=%d", prologueAnchor, interludeAnchor, chapter01Anchor)
	}
	if interludeAnchor < prologueAnchor {
		t.Error("interlude anchor should come after prologue anchor")
	}
	if interludeAnchor > chapter01Anchor {
		t.Error("interlude anchor should come before chapter_01 anchor")
	}
}

func TestStoryStore_Apply_Append(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	edit := StoryEdit{
		Target:  "narrative.md",
		Content: "<!-- id: epilogue -->\n## Epilogue\n\nThe end.",
	}
	if err := store.Apply(edit, "story-writer"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "story", "narrative.md"))
	content := string(data)
	epilogueIdx := strings.Index(content, "epilogue")
	chapter01Idx := strings.Index(content, "chapter_01")

	if epilogueIdx < chapter01Idx {
		t.Error("appended section should come after all existing sections")
	}
}

func TestStoryStore_Apply_MutuallyExclusive(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	edit := StoryEdit{
		Target:  "narrative.md",
		Section: "prologue",
		After:   "chapter_01",
		Content: "bad edit",
	}
	if err := store.Apply(edit, "writer"); err != ErrMutuallyExclusive {
		t.Errorf("expected ErrMutuallyExclusive, got %v", err)
	}
}

func TestStoryStore_Apply_AfterNotFound(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	edit := StoryEdit{
		Target:  "narrative.md",
		After:   "nonexistent",
		Content: "<!-- id: new -->\n## New\n\nContent.",
	}
	if err := store.Apply(edit, "writer"); err != ErrAfterNotFound {
		t.Errorf("expected ErrAfterNotFound, got %v", err)
	}
}

func TestStoryStore_Apply_PathTraversal(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	cases := []string{
		"../etc/passwd",
		"..\\etc\\passwd",
	}

	for _, target := range cases {
		edit := StoryEdit{Target: target, Content: "evil"}
		if err := store.Apply(edit, "attacker"); err != ErrPathTraversal {
			t.Errorf("target=%q: expected ErrPathTraversal, got %v", target, err)
		}
	}
}

func TestStoryStore_Apply_HumanEdit(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	edit := StoryEdit{
		Target:  "narrative.md",
		Section: "prologue",
		Content: "<!-- id: prologue -->\n## Prologue\n\nHuman-written content.",
	}
	if err := store.Apply(edit, "human"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "story", "narrative.md"))
	content := string(data)
	if !strings.Contains(content, "Human-written content.") {
		t.Error("human edit should be applied")
	}
	if !strings.Contains(content, "human") {
		t.Error("frontmatter should record human provenance")
	}
}

func TestStoryStore_Apply_HumanStaleRevRejected(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	edit := StoryEdit{
		Target:  "narrative.md",
		Section: "prologue",
		Rev:     "stale_hash",
		Content: "<!-- id: prologue -->\n## Prologue\n\nStale update.",
	}
	if err := store.CheckRevision(edit); err != ErrStaleRevision {
		t.Errorf("human stale rev should be rejected: %v", err)
	}
}

func TestStoryStore_Apply_HumanCurrentRevSucceeds(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	ctx, _ := store.ReadContext()
	rev := ctx.Sections["narrative.md"]["prologue"]

	edit := StoryEdit{
		Target:  "narrative.md",
		Section: "prologue",
		Rev:     rev,
		Content: "<!-- id: prologue -->\n## Prologue\n\nFresh human content.",
	}
	if err := store.CheckRevision(edit); err != nil {
		t.Fatalf("current rev should pass CheckRevision: %v", err)
	}
	if err := store.Apply(edit, "human"); err != nil {
		t.Fatalf("current rev apply should succeed: %v", err)
	}
}

func TestStoryStore_ReadContext(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	ctx, err := store.ReadContext()
	if err != nil {
		t.Fatal(err)
	}

	if ctx.Config == "" {
		t.Error("config should not be empty")
	}
	if !strings.Contains(ctx.Config, "TestGame") {
		t.Error("config should contain game name")
	}

	if len(ctx.Files) < 2 {
		t.Errorf("expected at least 2 files, got %d", len(ctx.Files))
	}

	if _, ok := ctx.Sections["narrative.md"]; !ok {
		t.Error("sections should include narrative.md")
	}
	if _, ok := ctx.Sections["narrative.md"]["prologue"]; !ok {
		t.Error("sections should include prologue")
	}
}

func TestStoryStore_Apply_AtomicWrite(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)

	origData, _ := os.ReadFile(filepath.Join(dir, "story", "narrative.md"))

	edit := StoryEdit{
		Target:  "narrative.md",
		Section: "prologue",
		Content: "<!-- id: prologue -->\n## Prologue\n\nUpdated.",
	}
	if err := store.Apply(edit, "writer"); err != nil {
		t.Fatal(err)
	}

	newData, _ := os.ReadFile(filepath.Join(dir, "story", "narrative.md"))
	if string(newData) == string(origData) {
		t.Error("file should have changed")
	}

	tmpFiles, _ := filepath.Glob(filepath.Join(dir, "story", "*.tmp"))
	if len(tmpFiles) > 0 {
		t.Error("temp file should be cleaned up")
	}
}
