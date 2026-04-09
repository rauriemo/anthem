package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidate_DuplicateIDsAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	storyDir := filepath.Join(dir, "story")
	if err := os.MkdirAll(storyDir, 0755); err != nil {
		t.Fatal(err)
	}

	file1 := `---
title: File 1
---

<!-- id: shared_id -->
## Section

Content.
`
	file2 := `---
title: File 2
---

<!-- id: shared_id -->
## Duplicate

Other content.
`
	if err := os.WriteFile(filepath.Join(storyDir, "a.md"), []byte(file1), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storyDir, "b.md"), []byte(file2), 0644); err != nil {
		t.Fatal(err)
	}

	store := NewStoryStore(dir)
	errs := store.Validate()

	found := false
	for _, e := range errs {
		if e.Level == "error" && e.Section == "shared_id" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected duplicate ID error")
	}
}

func TestValidate_MissingAnchor(t *testing.T) {
	dir := t.TempDir()
	storyDir := filepath.Join(dir, "story")
	if err := os.MkdirAll(storyDir, 0755); err != nil {
		t.Fatal(err)
	}

	file := `---
title: Test
---

<!-- id: sec1 -->
## Section 1

Content here.
`
	if err := os.WriteFile(filepath.Join(storyDir, "test.md"), []byte(file), 0644); err != nil {
		t.Fatal(err)
	}

	store := NewStoryStore(dir)
	errs := store.Validate()

	for _, e := range errs {
		if e.Level == "error" && e.Section == "" {
			t.Errorf("unexpected error for clean file: %s", e.Message)
		}
	}
}

func TestValidate_EmptySection(t *testing.T) {
	dir := t.TempDir()
	storyDir := filepath.Join(dir, "story")
	if err := os.MkdirAll(storyDir, 0755); err != nil {
		t.Fatal(err)
	}

	file := `---
title: Test
---

<!-- id: empty_one -->
## Empty Section
`
	if err := os.WriteFile(filepath.Join(storyDir, "test.md"), []byte(file), 0644); err != nil {
		t.Fatal(err)
	}

	store := NewStoryStore(dir)
	errs := store.Validate()

	found := false
	for _, e := range errs {
		if e.Level == "warning" && e.Section == "empty_one" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected empty section warning")
	}
}

func TestValidate_CleanFile(t *testing.T) {
	dir := setupStoryDir(t)
	store := NewStoryStore(dir)
	errs := store.Validate()

	for _, e := range errs {
		if e.Level == "error" {
			t.Errorf("clean file should have no errors: %s", e.Message)
		}
	}
}

func TestValidate_NonSnakeCaseInfo(t *testing.T) {
	dir := t.TempDir()
	storyDir := filepath.Join(dir, "story")
	if err := os.MkdirAll(storyDir, 0755); err != nil {
		t.Fatal(err)
	}

	file := `---
title: Test
---

<!-- id: CamelCase -->
## Bad ID

Content.
`
	if err := os.WriteFile(filepath.Join(storyDir, "test.md"), []byte(file), 0644); err != nil {
		t.Fatal(err)
	}

	store := NewStoryStore(dir)
	errs := store.Validate()

	found := false
	for _, e := range errs {
		if e.Level == "info" && e.Section == "CamelCase" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected info about non-snake_case ID")
	}
}
