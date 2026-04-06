package plans

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}

	body := "# My Plan\n\n## Tasks\n\n### 1. Do something\n"
	path, err := store.Save("rauriemo/prism", "Chat Mode Selector", body)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(path, ".md") {
		t.Errorf("expected .md suffix, got %s", path)
	}
	if !strings.Contains(path, "rauriemo-prism") {
		t.Errorf("expected project slug in path, got %s", path)
	}

	plan, err := store.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if plan.Frontmatter.Project != "rauriemo/prism" {
		t.Errorf("project = %q, want rauriemo/prism", plan.Frontmatter.Project)
	}
	if plan.Frontmatter.Status != StatusDraft {
		t.Errorf("status = %q, want draft", plan.Frontmatter.Status)
	}
	if plan.Frontmatter.Title != "Chat Mode Selector" {
		t.Errorf("title = %q, want Chat Mode Selector", plan.Frontmatter.Title)
	}
	if !strings.Contains(plan.Body, "# My Plan") {
		t.Errorf("body missing expected content, got %q", plan.Body)
	}
}

func TestUpdate(t *testing.T) {
	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}

	path, err := store.Save("owner/repo", "Test Plan", "original body")
	if err != nil {
		t.Fatal(err)
	}

	newBody := "# Updated Plan\n\nNew content here."
	if err := store.Update(path, newBody); err != nil {
		t.Fatal(err)
	}

	plan, err := store.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Body, "Updated Plan") {
		t.Errorf("body not updated, got %q", plan.Body)
	}
	if plan.Frontmatter.Title != "Test Plan" {
		t.Errorf("title changed unexpectedly to %q", plan.Frontmatter.Title)
	}
}

func TestSetStatus(t *testing.T) {
	tests := []struct {
		name   string
		status PlanStatus
	}{
		{"to building", StatusBuilding},
		{"to done", StatusDone},
		{"back to draft", StatusDraft},
	}

	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}

	path, err := store.Save("owner/repo", "Status Test", "body")
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.SetStatus(path, tt.status); err != nil {
				t.Fatal(err)
			}
			plan, err := store.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Frontmatter.Status != tt.status {
				t.Errorf("status = %q, want %q", plan.Frontmatter.Status, tt.status)
			}
		})
	}
}

func TestList(t *testing.T) {
	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.Save("owner/repo", "Plan A", "body a")
	if err != nil {
		t.Fatal(err)
	}
	pathB, err := store.Save("owner/repo", "Plan B", "body b")
	if err != nil {
		t.Fatal(err)
	}
	// Bump Plan B's Updated so it sorts first.
	if err := store.Update(pathB, "body b updated"); err != nil {
		t.Fatal(err)
	}
	_, err = store.Save("other/repo", "Plan C", "body c")
	if err != nil {
		t.Fatal(err)
	}

	metas, err := store.List("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Fatalf("expected 2 plans, got %d", len(metas))
	}
	// Most recent first
	if metas[0].Title != "Plan B" {
		t.Errorf("first plan = %q, want Plan B", metas[0].Title)
	}
}

func TestListEmptyProject(t *testing.T) {
	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}

	metas, err := store.List("nonexistent/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 0 {
		t.Errorf("expected 0 plans, got %d", len(metas))
	}
}

func TestLatestDraft(t *testing.T) {
	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}

	path1, _ := store.Save("owner/repo", "Old Plan", "old body")
	path2, _ := store.Save("owner/repo", "New Plan", "new body")

	_ = store.SetStatus(path1, StatusDone)

	plan, err := store.LatestDraft("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil {
		t.Fatal("expected a draft plan, got nil")
	}
	if plan.Path != path2 {
		t.Errorf("expected path %s, got %s", path2, plan.Path)
	}
}

func TestLatestDraftNone(t *testing.T) {
	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}

	path, _ := store.Save("owner/repo", "Done Plan", "body")
	_ = store.SetStatus(path, StatusDone)

	plan, err := store.LatestDraft("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if plan != nil {
		t.Errorf("expected nil, got plan %q", plan.Frontmatter.Title)
	}
}

func TestLoadNonexistent(t *testing.T) {
	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.Load(filepath.Join(home, "nope.md"))
	if err == nil {
		t.Error("expected error loading nonexistent file")
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Chat Mode Selector", "chat-mode-selector"},
		{"  Fix Bug #123  ", "fix-bug-123"},
		{"UPPERCASE", "uppercase"},
		{"", ""},
		{"a/b/c", "a-b-c"},
	}
	for _, tt := range tests {
		got := slugify(tt.input)
		if got != tt.want {
			t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNewStoreCreatesDirectory(t *testing.T) {
	home := t.TempDir()
	expected := filepath.Join(home, ".anthem", "plans")

	_, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(expected)
	if err != nil {
		t.Fatalf("directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory")
	}
}
