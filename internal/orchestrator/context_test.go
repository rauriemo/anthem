package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rauriemo/anthem/internal/cost"
)

func createFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("// stub"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateFileTree_BasicStructure(t *testing.T) {
	dir := t.TempDir()
	createFile(t, filepath.Join(dir, "src", "main.go"))
	createFile(t, filepath.Join(dir, "src", "util.go"))
	createFile(t, filepath.Join(dir, "pkg", "lib", "helper.go"))
	createFile(t, filepath.Join(dir, "README.md"))

	tree, err := generateFileTree(dir, 6)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"src/", "main.go", "util.go", "pkg/", "lib/", "helper.go", "README.md"} {
		if !strings.Contains(tree, want) {
			t.Errorf("tree missing %q\n%s", want, tree)
		}
	}

	// Directories before files at the same level: pkg/ and src/ should appear before README.md
	pkgIdx := strings.Index(tree, "pkg/")
	srcIdx := strings.Index(tree, "src/")
	readmeIdx := strings.Index(tree, "README.md")
	if pkgIdx > readmeIdx || srcIdx > readmeIdx {
		t.Errorf("directories should appear before files at same level\n%s", tree)
	}
}

func TestGenerateFileTree_SkipsExcludedDirs(t *testing.T) {
	dir := t.TempDir()
	createFile(t, filepath.Join(dir, "src", "main.go"))
	createFile(t, filepath.Join(dir, ".git", "config"))
	createFile(t, filepath.Join(dir, "node_modules", "pkg", "index.js"))
	createFile(t, filepath.Join(dir, "vendor", "lib.go"))

	tree, err := generateFileTree(dir, 6)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(tree, "src/") {
		t.Errorf("tree should contain src/\n%s", tree)
	}
	if !strings.Contains(tree, "main.go") {
		t.Errorf("tree should contain main.go\n%s", tree)
	}

	for _, excluded := range []string{".git", "node_modules", "vendor"} {
		if strings.Contains(tree, excluded) {
			t.Errorf("tree should not contain %q\n%s", excluded, tree)
		}
	}
}

func TestGenerateFileTree_RespectsMaxDepth(t *testing.T) {
	dir := t.TempDir()
	// a/b/c/d/e/f/deep.go — depth 1=a, 2=b, 3=c, 4=d, 5=e, 6=f, 7=deep.go
	createFile(t, filepath.Join(dir, "a", "b", "c", "d", "e", "f", "deep.go"))

	tree, err := generateFileTree(dir, 3)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"a/", "b/", "c/"} {
		if !strings.Contains(tree, want) {
			t.Errorf("tree should contain %q\n%s", want, tree)
		}
	}
	for _, excluded := range []string{"d/", "deep.go"} {
		if strings.Contains(tree, excluded) {
			t.Errorf("tree should not contain %q at maxDepth=3\n%s", excluded, tree)
		}
	}
}

func TestGenerateFileTree_Truncation(t *testing.T) {
	dir := t.TempDir()
	// Create 500 files to exceed 8KB
	for i := 0; i < 500; i++ {
		createFile(t, filepath.Join(dir, "pkg", fmt.Sprintf("file_%04d_with_a_long_name.go", i)))
	}

	tree, err := generateFileTree(dir, 6)
	if err != nil {
		t.Fatal(err)
	}

	maxAllowed := maxTreeBytes + len("... (truncated)\n")
	if len(tree) > maxAllowed {
		t.Errorf("tree length %d exceeds max allowed %d", len(tree), maxAllowed)
	}
	if !strings.HasSuffix(strings.TrimRight(tree, "\n"), "... (truncated)") {
		t.Errorf("truncated tree should end with '... (truncated)'\n...last 100 chars: %q", tree[len(tree)-100:])
	}
}

func TestBuildStateSnapshot_IncludesProjectContext(t *testing.T) {
	o := &Orchestrator{
		costTracker: cost.NewTracker(),
		retryState:  make(map[string]*RetryInfo),
		projectCtx: &ProjectContext{
			FileTree:       "cmd/\n  main.go\n",
			ProjectSummary: "test summary",
		},
	}

	snap := o.buildStateSnapshot(nil)

	if snap.Project == nil {
		t.Fatal("snap.Project should not be nil")
	}
	if snap.Project.FileTree != "cmd/\n  main.go\n" {
		t.Errorf("unexpected FileTree: %q", snap.Project.FileTree)
	}
	if snap.Project.ProjectSummary != "test summary" {
		t.Errorf("unexpected ProjectSummary: %q", snap.Project.ProjectSummary)
	}
}

func TestBuildSystemPrompt_ContainsProjectContextSection(t *testing.T) {
	prompt := buildSystemPrompt("")

	if !strings.Contains(prompt, "## Project Context") {
		t.Error("system prompt should contain '## Project Context'")
	}
	if !strings.Contains(prompt, "file_tree") {
		t.Error("system prompt should contain 'file_tree'")
	}
}
