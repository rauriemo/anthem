package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func seedTaskState(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `schema_version: "1"
feature: test-feature
phase: concept
updated_at: "2026-01-01T00:00:00Z"
agents:
  miyazaki:
    status: idle
    current_task: ""
    last_output: ""
    last_updated: "2026-01-01T00:00:00Z"
  shigeru:
    status: idle
    current_task: ""
    last_output: ""
    last_updated: "2026-01-01T00:00:00Z"
`
	if err := os.WriteFile(filepath.Join(dir, "task-state.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func seedArtifacts(t *testing.T, dir string, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "artifacts.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateTaskState(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedTaskState(t, dir)

	if err := UpdateTaskState(root, feature, "miyazaki", "idle", "sprites/goblin.png"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "task-state.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var ts TaskStateFile
	if err := yaml.Unmarshal(data, &ts); err != nil {
		t.Fatal(err)
	}

	agent, ok := ts.Agents["miyazaki"]
	if !ok {
		t.Fatal("miyazaki not found in task-state")
	}
	if agent.Status != "idle" {
		t.Errorf("status = %q, want idle", agent.Status)
	}
	if agent.LastOutput != "sprites/goblin.png" {
		t.Errorf("last_output = %q, want sprites/goblin.png", agent.LastOutput)
	}
	if agent.LastUpdated == "" || agent.LastUpdated == "2026-01-01T00:00:00Z" {
		t.Error("last_updated was not refreshed")
	}

	shigeru, ok := ts.Agents["shigeru"]
	if !ok {
		t.Fatal("shigeru should still exist")
	}
	if shigeru.Status != "idle" {
		t.Errorf("shigeru status = %q, want idle (unchanged)", shigeru.Status)
	}
}

func TestSetTaskActive(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedTaskState(t, dir)

	if err := SetTaskActive(root, feature, "miyazaki", "Generate goblin sprites"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "task-state.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var ts TaskStateFile
	if err := yaml.Unmarshal(data, &ts); err != nil {
		t.Fatal(err)
	}

	agent := ts.Agents["miyazaki"]
	if agent.Status != "active" {
		t.Errorf("status = %q, want active", agent.Status)
	}
	if agent.CurrentTask != "Generate goblin sprites" {
		t.Errorf("current_task = %q, want 'Generate goblin sprites'", agent.CurrentTask)
	}
}

func TestUpdateTaskState_ConcurrentWrites(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedTaskState(t, dir)

	var wg sync.WaitGroup
	agents := []string{"miyazaki", "shigeru"}

	for i := 0; i < 20; i++ {
		for _, name := range agents {
			wg.Add(1)
			go func(n string, idx int) {
				defer wg.Done()
				_ = UpdateTaskState(root, feature, n, "idle", fmt.Sprintf("output-%s-%d", n, idx))
			}(name, i)
		}
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(dir, "task-state.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var ts TaskStateFile
	if err := yaml.Unmarshal(data, &ts); err != nil {
		t.Fatalf("concurrent writes produced invalid YAML: %v", err)
	}

	if len(ts.Agents) < 2 {
		t.Errorf("expected at least 2 agents, got %d", len(ts.Agents))
	}
}

func TestAppendArtifact(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedArtifacts(t, dir, `schema_version: "1"
artifacts:
  - id: existing-001
    type: image/png
    path: assets/existing.png
    created_by: miyazaki
    status: approved
    description: Existing asset
`)

	entry := ArtifactEntry{
		ID:          "new-001",
		Type:        "image/png",
		Path:        "assets/goblin.png",
		CreatedBy:   "miyazaki",
		Feature:     feature,
		Status:      "pending-review",
		Description: "Goblin sprite sheet",
		Tags:        []string{"sprite", "enemy"},
	}

	if err := AppendArtifact(root, feature, entry); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "artifacts.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var af ArtifactsFile
	if err := yaml.Unmarshal(data, &af); err != nil {
		t.Fatal(err)
	}

	if len(af.Artifacts) != 2 {
		t.Fatalf("expected 2 artifacts, got %d", len(af.Artifacts))
	}
	if af.Artifacts[1].ID != "new-001" {
		t.Errorf("second artifact ID = %q, want new-001", af.Artifacts[1].ID)
	}
	if len(af.Artifacts[1].Tags) != 2 || af.Artifacts[1].Tags[0] != "sprite" {
		t.Errorf("tags = %v, want [sprite enemy]", af.Artifacts[1].Tags)
	}
}

func TestAppendArtifact_ToEmptyFile(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedArtifacts(t, dir, `schema_version: "1"
artifacts: []
`)

	entry := ArtifactEntry{
		ID:          "first-001",
		Type:        "image/png",
		Path:        "assets/first.png",
		CreatedBy:   "miyazaki",
		Status:      "pending-review",
		Description: "First artifact",
	}

	if err := AppendArtifact(root, feature, entry); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "artifacts.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var af ArtifactsFile
	if err := yaml.Unmarshal(data, &af); err != nil {
		t.Fatal(err)
	}

	if len(af.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(af.Artifacts))
	}
	if af.Artifacts[0].ID != "first-001" {
		t.Errorf("artifact ID = %q, want first-001", af.Artifacts[0].ID)
	}
}

func TestAppendArtifact_FileNotExist(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	entry := ArtifactEntry{
		ID:     "from-scratch",
		Type:   "text/plain",
		Path:   "output.txt",
		Status: "pending-review",
	}

	if err := AppendArtifact(root, feature, entry); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "artifacts.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var af ArtifactsFile
	if err := yaml.Unmarshal(data, &af); err != nil {
		t.Fatal(err)
	}

	if af.SchemaVersion != "1" {
		t.Errorf("schema_version = %q, want 1", af.SchemaVersion)
	}
	if len(af.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(af.Artifacts))
	}
}

func TestSanitizeSavePath_Valid(t *testing.T) {
	root := t.TempDir()
	cases := []string{
		"assets/sprites/goblin.png",
		"Assets/Generated/output.png",
		"simple.txt",
	}
	for _, input := range cases {
		result, err := SanitizeSavePath(root, input)
		if err != nil {
			t.Errorf("SanitizeSavePath(%q) unexpected error: %v", input, err)
			continue
		}
		if !filepath.IsAbs(result) {
			t.Errorf("SanitizeSavePath(%q) = %q, want absolute path", input, result)
		}
	}
}

func TestSanitizeSavePath_TraversalRejected(t *testing.T) {
	root := t.TempDir()
	cases := []string{
		"../etc/passwd",
		"assets/../../secret",
		"foo/../../../bar",
	}
	for _, input := range cases {
		_, err := SanitizeSavePath(root, input)
		if err == nil {
			t.Errorf("SanitizeSavePath(%q) should have been rejected", input)
		}
	}
}

func TestSanitizeSavePath_AbsoluteRejected(t *testing.T) {
	root := t.TempDir()

	abs, _ := filepath.Abs(filepath.Join(root, "..", "outside", "file.txt"))
	_, err := SanitizeSavePath(root, abs)
	if err == nil {
		t.Errorf("SanitizeSavePath(%q) should reject absolute paths", abs)
	}
}

func TestSanitizeSavePath_EmptyString(t *testing.T) {
	root := t.TempDir()
	result, err := SanitizeSavePath(root, "")
	if err != nil {
		return
	}
	absRoot, _ := filepath.Abs(root)
	if result == absRoot {
		return
	}
	t.Errorf("SanitizeSavePath(\"\") = (%q, %v); expected either error or root", result, err)
}

func TestSanitizeSavePath_DotPath(t *testing.T) {
	root := t.TempDir()
	_, err := SanitizeSavePath(root, ".")
	if err != nil {
		return
	}
}

func TestUpdateTaskState_MissingFile(t *testing.T) {
	root := t.TempDir()
	feature := "nonexistent"
	err := UpdateTaskState(root, feature, "miyazaki", "idle", "output")
	if err == nil {
		t.Fatal("expected error when task-state.yaml does not exist")
	}
}

func TestSetTaskActive_MissingFile(t *testing.T) {
	root := t.TempDir()
	feature := "nonexistent"
	err := SetTaskActive(root, feature, "miyazaki", "Generate sprites")
	if err == nil {
		t.Fatal("expected error when task-state.yaml does not exist")
	}
}

func TestUpdateTaskState_NewAgent(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedTaskState(t, dir)

	if err := UpdateTaskState(root, feature, "eiji", "idle", "scene placed"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "task-state.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var ts TaskStateFile
	if err := yaml.Unmarshal(data, &ts); err != nil {
		t.Fatal(err)
	}

	if len(ts.Agents) != 3 {
		t.Errorf("expected 3 agents (miyazaki, shigeru, eiji), got %d", len(ts.Agents))
	}
	eiji, ok := ts.Agents["eiji"]
	if !ok {
		t.Fatal("eiji not found after update")
	}
	if eiji.LastOutput != "scene placed" {
		t.Errorf("eiji last_output = %q, want 'scene placed'", eiji.LastOutput)
	}
}

func TestUpdateTaskState_EmptyOutput(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedTaskState(t, dir)

	if err := UpdateTaskState(root, feature, "miyazaki", "idle", ""); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "task-state.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var ts TaskStateFile
	if err := yaml.Unmarshal(data, &ts); err != nil {
		t.Fatal(err)
	}

	if ts.Agents["miyazaki"].LastOutput != "" {
		t.Errorf("last_output = %q, want empty string", ts.Agents["miyazaki"].LastOutput)
	}
}

func TestUpdateTaskState_InvalidYAML(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task-state.yaml"), []byte("{{invalid yaml"), 0644); err != nil {
		t.Fatal(err)
	}

	err := UpdateTaskState(root, feature, "miyazaki", "idle", "output")
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestAppendArtifact_ConcurrentWrites(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedArtifacts(t, dir, `schema_version: "1"
artifacts: []
`)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			entry := ArtifactEntry{
				ID:     fmt.Sprintf("art-%03d", idx),
				Type:   "image/png",
				Path:   fmt.Sprintf("assets/sprite-%d.png", idx),
				Status: "pending-review",
			}
			_ = AppendArtifact(root, feature, entry)
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(dir, "artifacts.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var af ArtifactsFile
	if err := yaml.Unmarshal(data, &af); err != nil {
		t.Fatalf("concurrent writes produced invalid YAML: %v", err)
	}

	if len(af.Artifacts) != 20 {
		t.Errorf("expected 20 artifacts, got %d", len(af.Artifacts))
	}
}

func TestTruncateOutput(t *testing.T) {
	cases := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"", 10, ""},
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello!", 5, "hello..."},
		{"abcdefghij", 10, "abcdefghij"},
		{"abcdefghijk", 10, "abcdefghij..."},
	}
	for _, tc := range cases {
		got := truncateOutput(tc.input, tc.maxLen)
		if got != tc.want {
			t.Errorf("truncateOutput(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
		}
	}
}

func TestCrossFeatureLocking(t *testing.T) {
	root := t.TempDir()
	featureA := "feature-a"
	featureB := "feature-b"
	dirA := filepath.Join(root, ".context", "features", featureA)
	dirB := filepath.Join(root, ".context", "features", featureB)
	seedTaskState(t, dirA)
	seedTaskState(t, dirB)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			_ = UpdateTaskState(root, featureA, "miyazaki", "idle", fmt.Sprintf("a-%d", idx))
		}(i)
		go func(idx int) {
			defer wg.Done()
			_ = UpdateTaskState(root, featureB, "shigeru", "idle", fmt.Sprintf("b-%d", idx))
		}(i)
	}
	wg.Wait()

	for _, pair := range []struct {
		feature string
		dir     string
	}{
		{featureA, dirA},
		{featureB, dirB},
	} {
		data, err := os.ReadFile(filepath.Join(pair.dir, "task-state.yaml"))
		if err != nil {
			t.Fatalf("reading %s: %v", pair.feature, err)
		}
		var ts TaskStateFile
		if err := yaml.Unmarshal(data, &ts); err != nil {
			t.Fatalf("cross-feature writes to %s produced invalid YAML: %v", pair.feature, err)
		}
	}
}
