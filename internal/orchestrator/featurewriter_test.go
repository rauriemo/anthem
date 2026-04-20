package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

	if err := AppendArtifact(root, feature, entry, ChatOrigin{AgentID: "miyazaki"}); err != nil {
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

	if err := AppendArtifact(root, feature, entry, ChatOrigin{AgentID: "miyazaki"}); err != nil {
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

	if err := AppendArtifact(root, feature, entry, ChatOrigin{AgentID: "miyazaki"}); err != nil {
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
			_ = AppendArtifact(root, feature, entry, ChatOrigin{AgentID: "miyazaki"})
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

func TestAppendChangelog(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedTaskState(t, dir)

	changelogPath := filepath.Join(dir, "changelog.yaml")
	if _, err := os.Stat(changelogPath); err == nil {
		t.Fatal("changelog.yaml should not exist before AppendChangelog")
	}

	entry := ChangelogEntry{
		Agent:   "miyazaki",
		Action:  "asset_created",
		Summary: "Created sprite",
	}
	if err := AppendChangelog(root, feature, entry); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(changelogPath)
	if err != nil {
		t.Fatal(err)
	}

	var cf ChangelogFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		t.Fatal(err)
	}
	if cf.SchemaVersion != "1" {
		t.Errorf("schema_version = %q, want 1", cf.SchemaVersion)
	}
	if len(cf.Entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(cf.Entries))
	}
	if cf.Entries[0].ID != "log-001" {
		t.Errorf("entry id = %q, want log-001", cf.Entries[0].ID)
	}
	if cf.Entries[0].Agent != "miyazaki" || cf.Entries[0].Action != "asset_created" || cf.Entries[0].Summary != "Created sprite" {
		t.Errorf("entry = %+v", cf.Entries[0])
	}
}

func TestAppendChangelog_ToEmptyFile(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "changelog.yaml"), []byte(`schema_version: "1"
entries: []
`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := AppendChangelog(root, feature, ChangelogEntry{Agent: "eiji", Action: "scene_edit", Summary: "placed tower"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "changelog.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var cf ChangelogFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		t.Fatal(err)
	}
	if len(cf.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(cf.Entries))
	}
}

func TestAppendChangelog_ConcurrentWrites(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "changelog.yaml"), []byte(`schema_version: "1"
entries: []
`), 0644); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = AppendChangelog(root, feature, ChangelogEntry{
				Agent:   "miyazaki",
				Action:  "log",
				Summary: fmt.Sprintf("entry-%d", idx),
			})
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(dir, "changelog.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var cf ChangelogFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		t.Fatalf("concurrent writes produced invalid YAML: %v", err)
	}
	if len(cf.Entries) != 20 {
		t.Errorf("expected 20 entries, got %d", len(cf.Entries))
	}
}

func TestSetTaskActive_WithDependencies(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedTaskState(t, dir)

	opts := TaskActiveOpts{
		Dependencies: []string{"sprite-goblin"},
		Produces:     []string{"scene-level-1"},
	}
	if err := SetTaskActive(root, feature, "miyazaki", "Build scene", opts); err != nil {
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
	if !reflect.DeepEqual(agent.Dependencies, []string{"sprite-goblin"}) {
		t.Errorf("dependencies = %v", agent.Dependencies)
	}
	if !reflect.DeepEqual(agent.Produces, []string{"scene-level-1"}) {
		t.Errorf("produces = %v", agent.Produces)
	}
}

func TestUpdateTaskState_WithProgress(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedTaskState(t, dir)

	upd := TaskStateUpdate{
		Progress:  "3/8 done",
		BlockedOn: "dec-002",
		Produces:  []string{"sprite-orc"},
	}
	if err := UpdateTaskState(root, feature, "miyazaki", "active", "sprites/orc.png", upd); err != nil {
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
	if agent.Progress != "3/8 done" {
		t.Errorf("progress = %q", agent.Progress)
	}
	if agent.BlockedOn != "dec-002" {
		t.Errorf("blocked_on = %q", agent.BlockedOn)
	}
	if !reflect.DeepEqual(agent.Produces, []string{"sprite-orc"}) {
		t.Errorf("produces = %v", agent.Produces)
	}
}

func TestUpdateTaskProgress(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedTaskState(t, dir)

	if err := SetTaskActive(root, feature, "miyazaki", "Art pass"); err != nil {
		t.Fatal(err)
	}
	if err := UpdateTaskProgress(root, feature, "miyazaki", "5/8 done", ""); err != nil {
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
	if agent.Progress != "5/8 done" {
		t.Errorf("progress = %q, want 5/8 done", agent.Progress)
	}
}

func TestAppendArtifact_WithRichMetadata(t *testing.T) {
	root := t.TempDir()
	feature := "test-feature"
	dir := filepath.Join(root, ".context", "features", feature)
	seedArtifacts(t, dir, `schema_version: "1"
artifacts: []
`)

	entry := ArtifactEntry{
		ID:          "rich-001",
		Type:        "image/png",
		Path:        "assets/sheet.png",
		CreatedBy:   "miyazaki",
		Status:      "pending-review",
		Description: "Sprite with metadata",
		Metadata:    map[string]string{"width": "64", "frames": "4"},
		DependsOn:   []string{"concept-art"},
		Consumers:   []string{"eiji"},
	}

	if err := AppendArtifact(root, feature, entry, ChatOrigin{AgentID: "miyazaki"}); err != nil {
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
	got := af.Artifacts[0]
	if !reflect.DeepEqual(got.Metadata, map[string]string{"width": "64", "frames": "4"}) {
		t.Errorf("metadata = %v", got.Metadata)
	}
	if !reflect.DeepEqual(got.DependsOn, []string{"concept-art"}) {
		t.Errorf("depends_on = %v", got.DependsOn)
	}
	if !reflect.DeepEqual(got.Consumers, []string{"eiji"}) {
		t.Errorf("consumers = %v", got.Consumers)
	}
}
