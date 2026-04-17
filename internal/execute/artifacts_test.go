package execute

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestContextArtifactProvider_CollectEmpty(t *testing.T) {
	dir := t.TempDir()
	p := NewContextArtifactProvider(dir, "test-feature")
	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(arts) != 0 {
		t.Fatalf("expected 0 artifacts, got %d", len(arts))
	}
}

func TestContextArtifactProvider_CollectByCreatedBy(t *testing.T) {
	dir := t.TempDir()
	featureDir := filepath.Join(dir, ".context", "features", "test-feature")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}

	entries := contextArtifactsFile{
		SchemaVersion: "2",
		Artifacts: []contextArtifactEntry{
			{ID: "a1", Type: "image", Path: "sprites/hero.png", CreatedBy: "s1", Desc: "Hero sprite"},
			{ID: "a2", Type: "file", Path: "code/main.go", CreatedBy: "s2", Desc: "Main file"},
		},
	}
	data, _ := yaml.Marshal(entries)
	if err := os.WriteFile(filepath.Join(featureDir, "artifacts.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}

	p := NewContextArtifactProvider(dir, "test-feature")
	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(arts))
	}
	if arts[0].Path != "sprites/hero.png" {
		t.Errorf("unexpected path: %s", arts[0].Path)
	}
}

func TestContextArtifactProvider_Inject(t *testing.T) {
	dir := t.TempDir()
	p := NewContextArtifactProvider(dir, "test-feature")

	upstream := []StepArtifact{
		{StepID: "s1", Path: "sprites/hero.png", Kind: "image", Summary: "Hero sprite"},
	}
	if err := p.Inject("s2", upstream); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	path := filepath.Join(dir, ".context", "features", "test-feature", "step-s2-upstream.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("manifest not written: %v", err)
	}

	var manifest upstreamManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("invalid manifest: %v", err)
	}
	if manifest.StepID != "s2" {
		t.Errorf("expected step s2, got %s", manifest.StepID)
	}
	if len(manifest.Upstream) != 1 {
		t.Fatalf("expected 1 upstream, got %d", len(manifest.Upstream))
	}
}

func TestContextArtifactProvider_InjectEmpty(t *testing.T) {
	dir := t.TempDir()
	p := NewContextArtifactProvider(dir, "test-feature")
	if err := p.Inject("s1", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	path := filepath.Join(dir, ".context", "features", "test-feature", "step-s1-upstream.yaml")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("empty inject should not create file")
	}
}

func TestFilesystemArtifactProvider_CollectNewFile(t *testing.T) {
	dir := t.TempDir()
	p := NewFilesystemArtifactProvider(dir)

	p.MarkStepStart("s1")

	// Simulate agent creating a file
	time.Sleep(10 * time.Millisecond)
	newFile := filepath.Join(dir, "output.png")
	if err := os.WriteFile(newFile, []byte("img"), 0644); err != nil {
		t.Fatal(err)
	}

	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(arts))
	}
	if arts[0].Kind != "image" {
		t.Errorf("expected kind image, got %s", arts[0].Kind)
	}
}

func TestFilesystemArtifactProvider_CollectNoSnapshot(t *testing.T) {
	dir := t.TempDir()
	p := NewFilesystemArtifactProvider(dir)
	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(arts) != 0 {
		t.Fatalf("expected 0 artifacts without snapshot, got %d", len(arts))
	}
}

func TestFilesystemArtifactProvider_Inject(t *testing.T) {
	dir := t.TempDir()
	p := NewFilesystemArtifactProvider(dir)

	upstream := []StepArtifact{
		{StepID: "s1", Path: "output.png", Kind: "image", Summary: "Img"},
	}
	if err := p.Inject("s2", upstream); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	path := filepath.Join(dir, ".context", "execution", "step-s2-upstream.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("manifest not created: %v", err)
	}
}

func TestKindFromExt(t *testing.T) {
	tests := []struct {
		ext  string
		want string
	}{
		{".png", "image"},
		{".PNG", "image"},
		{".mp4", "animation"},
		{".unity", "scene"},
		{".go", "file"},
		{".txt", "file"},
	}
	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			if got := kindFromExt(tt.ext); got != tt.want {
				t.Errorf("kindFromExt(%q) = %q, want %q", tt.ext, got, tt.want)
			}
		})
	}
}
