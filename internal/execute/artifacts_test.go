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

// TestContextArtifactProvider_AdoptedArtifactIsCaptured pins the exact
// failure surface reported in practice: Miyazaki "adopted" an existing
// sprite by appending an entry to artifacts.yaml whose `created_by` is
// its agent id ("miyazaki") and whose `created_at` is earlier than when
// the step actually started. Both the legacy CreatedBy==stepID filter
// and the t.After(marker) filter would drop this entry -- Collect must
// still return it because its ID is new since MarkStepStart snapshot.
func TestContextArtifactProvider_AdoptedArtifactIsCaptured(t *testing.T) {
	dir := t.TempDir()
	featureDir := filepath.Join(dir, ".context", "features", "tower-defense-level-1")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Snapshot state: no hero sprite yet.
	initial := contextArtifactsFile{
		SchemaVersion: "1",
		Artifacts: []contextArtifactEntry{
			{ID: "battle-king-idle", Type: "image/png", Path: "Assets/Art/battle-king.png", CreatedBy: "miyazaki", CreatedAt: "2026-04-12T00:00:00Z"},
		},
	}
	data, _ := yaml.Marshal(initial)
	artifactsPath := filepath.Join(featureDir, "artifacts.yaml")
	if err := os.WriteFile(artifactsPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	p := NewContextArtifactProvider(dir, "tower-defense-level-1")
	p.MarkStepStart("s1")

	// The agent runs, then appends a new entry. created_by is the agent
	// id (not the step id), and created_at is deliberately earlier than
	// "now" to reproduce the fabricated-timestamp case (adopted artifact
	// or planning-document backdating).
	final := contextArtifactsFile{
		SchemaVersion: "1",
		Artifacts: []contextArtifactEntry{
			initial.Artifacts[0],
			{
				ID:        "character-sprite-hero-child-idle-se",
				Type:      "image/png",
				Path:      "Assets/Art/hero-child-idle.png",
				CreatedBy: "miyazaki",
				CreatedAt: "2020-01-01T00:00:00Z", // earlier than MarkStepStart
				Status:    "approved",
				Desc:      "Adopted SE-facing hero sprite",
			},
		},
	}
	data, _ = yaml.Marshal(final)
	if err := os.WriteFile(artifactsPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("expected 1 new artifact since snapshot, got %d: %+v", len(arts), arts)
	}
	if arts[0].Path != "Assets/Art/hero-child-idle.png" {
		t.Errorf("unexpected path: %q", arts[0].Path)
	}
	if arts[0].Kind != "image/png" {
		t.Errorf("unexpected kind: %q", arts[0].Kind)
	}
	if arts[0].Summary != "Adopted SE-facing hero sprite" {
		t.Errorf("unexpected summary: %q", arts[0].Summary)
	}
}

// TestContextArtifactProvider_PreExistingEntriesAreNotReReported guards
// against the failure mode where the snapshot isn't taken (or is empty)
// and the provider falsely claims every entry belongs to the current
// step. Pre-existing entries must stay invisible even when their
// created_by matches the agent running this step.
func TestContextArtifactProvider_PreExistingEntriesAreNotReReported(t *testing.T) {
	dir := t.TempDir()
	featureDir := filepath.Join(dir, ".context", "features", "test-feature")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}

	initial := contextArtifactsFile{
		SchemaVersion: "1",
		Artifacts: []contextArtifactEntry{
			{ID: "old-1", Type: "image", Path: "a.png", CreatedBy: "miyazaki", CreatedAt: "2026-04-12T00:00:00Z"},
			{ID: "old-2", Type: "image", Path: "b.png", CreatedBy: "miyazaki", CreatedAt: "2026-04-13T00:00:00Z"},
		},
	}
	data, _ := yaml.Marshal(initial)
	if err := os.WriteFile(filepath.Join(featureDir, "artifacts.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}

	p := NewContextArtifactProvider(dir, "test-feature")
	p.MarkStepStart("s1")
	// No new entries appended -- step was a pure no-op.

	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(arts) != 0 {
		t.Fatalf("pre-existing entries must not surface, got %d: %+v", len(arts), arts)
	}
}

// TestContextArtifactProvider_ReviseMutatesInPlace pins the exact
// failure surface that was observed in practice: on a Revise cycle the
// agent regenerates the binary payload (the PNG on disk) and updates
// the existing artifacts.yaml entry in place rather than appending a
// new one. A pure ID-set diff would return zero artifacts and the
// review drawer would say "No images found for this gate". The
// fingerprint diff must surface the entry because the file's mtime and
// size changed during the step even though the ID did not.
func TestContextArtifactProvider_ReviseMutatesInPlace(t *testing.T) {
	dir := t.TempDir()
	featureDir := filepath.Join(dir, ".context", "features", "tower-defense-level-1")
	assetDir := filepath.Join(dir, "Assets", "Art")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(assetDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Initial (pre-step) state: the N-idle sprite already exists -- it
	// was produced in an earlier run of the same step that the user
	// then clicked Revise on. The binary payload and yaml entry both
	// predate MarkStepStart.
	spritePath := filepath.Join(assetDir, "hero-idle-n.png")
	if err := os.WriteFile(spritePath, []byte("version-1-bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(spritePath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	initial := contextArtifactsFile{
		SchemaVersion: "1",
		Artifacts: []contextArtifactEntry{
			{
				ID:        "character-sprite-hero-idle-n",
				Type:      "image/png",
				Path:      "Assets/Art/hero-idle-n.png",
				CreatedBy: "miyazaki",
				CreatedAt: "2026-04-18T17:15:00Z",
				UpdatedAt: "2026-04-18T17:15:00Z",
				Status:    "pending-review",
				Desc:      "Hood up",
			},
		},
	}
	data, _ := yaml.Marshal(initial)
	artifactsPath := filepath.Join(featureDir, "artifacts.yaml")
	if err := os.WriteFile(artifactsPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	p := NewContextArtifactProvider(dir, "tower-defense-level-1")
	p.MarkStepStart("s1")

	// Agent runs a Revise: regenerates the PNG (different bytes, new
	// mtime) and updates the existing yaml entry in place -- same ID,
	// bumped updated_at, tweaked description. A pure ID-set diff would
	// miss this.
	if err := os.WriteFile(spritePath, []byte("version-2-hood-down-bytes-longer"), 0644); err != nil {
		t.Fatal(err)
	}
	newTime := time.Now()
	if err := os.Chtimes(spritePath, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	revised := contextArtifactsFile{
		SchemaVersion: "1",
		Artifacts: []contextArtifactEntry{
			{
				ID:        "character-sprite-hero-idle-n",
				Type:      "image/png",
				Path:      "Assets/Art/hero-idle-n.png",
				CreatedBy: "miyazaki",
				CreatedAt: "2026-04-18T17:15:00Z",
				UpdatedAt: "2026-04-18T20:14:00Z", // bumped
				Status:    "pending-review",
				Desc:      "Hood down",
			},
		},
	}
	data, _ = yaml.Marshal(revised)
	if err := os.WriteFile(artifactsPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("revised-in-place entry must surface, got %d: %+v", len(arts), arts)
	}
	if arts[0].Summary != "Hood down" {
		t.Errorf("expected updated description, got %q", arts[0].Summary)
	}
}

// TestContextArtifactProvider_ReviseCatchesFileOnlyMutation guards the
// cheapest-signal path: an agent rewrites the binary payload but
// forgets to bump updated_at or status. The file-mtime component of
// the fingerprint must still flip the entry "dirty".
func TestContextArtifactProvider_ReviseCatchesFileOnlyMutation(t *testing.T) {
	dir := t.TempDir()
	featureDir := filepath.Join(dir, ".context", "features", "f")
	assetDir := filepath.Join(dir, "Assets")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(assetDir, 0755); err != nil {
		t.Fatal(err)
	}

	spritePath := filepath.Join(assetDir, "x.png")
	if err := os.WriteFile(spritePath, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-30 * time.Minute)
	if err := os.Chtimes(spritePath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	entries := contextArtifactsFile{
		SchemaVersion: "1",
		Artifacts: []contextArtifactEntry{
			{ID: "x", Type: "image", Path: "Assets/x.png", CreatedBy: "a", UpdatedAt: "2026-04-18T17:00:00Z"},
		},
	}
	data, _ := yaml.Marshal(entries)
	if err := os.WriteFile(filepath.Join(featureDir, "artifacts.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}

	p := NewContextArtifactProvider(dir, "f")
	p.MarkStepStart("s1")

	// Rewrite the file only. artifacts.yaml is byte-identical after the
	// step -- updated_at, status, path, everything matches the snapshot
	// except the file on disk.
	if err := os.WriteFile(spritePath, []byte("v2-different"), 0644); err != nil {
		t.Fatal(err)
	}
	newTime := time.Now()
	if err := os.Chtimes(spritePath, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("file-only mutation must be detected via mtime/size, got %d", len(arts))
	}
}

// TestContextArtifactProvider_TrueNoopDoesNotSurface guards against
// the opposite failure: if nothing changed (yaml untouched, file
// untouched), Collect must return zero. Otherwise every step would
// re-surface every pre-existing artifact.
func TestContextArtifactProvider_TrueNoopDoesNotSurface(t *testing.T) {
	dir := t.TempDir()
	featureDir := filepath.Join(dir, ".context", "features", "f")
	assetDir := filepath.Join(dir, "Assets")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(assetDir, 0755); err != nil {
		t.Fatal(err)
	}

	spritePath := filepath.Join(assetDir, "x.png")
	if err := os.WriteFile(spritePath, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-30 * time.Minute)
	if err := os.Chtimes(spritePath, t0, t0); err != nil {
		t.Fatal(err)
	}

	entries := contextArtifactsFile{
		SchemaVersion: "1",
		Artifacts: []contextArtifactEntry{
			{ID: "x", Type: "image", Path: "Assets/x.png", CreatedBy: "a", UpdatedAt: "2026-04-18T17:00:00Z"},
		},
	}
	data, _ := yaml.Marshal(entries)
	if err := os.WriteFile(filepath.Join(featureDir, "artifacts.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}

	p := NewContextArtifactProvider(dir, "f")
	p.MarkStepStart("s1")
	// No mutation at all.

	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(arts) != 0 {
		t.Fatalf("pure no-op must not surface anything, got %d", len(arts))
	}
}

// TestContextArtifactProvider_MultipleNewEntries verifies the primary
// workflow: a guest appends several artifacts during one step and all of
// them surface to the approval gate.
func TestContextArtifactProvider_MultipleNewEntries(t *testing.T) {
	dir := t.TempDir()
	featureDir := filepath.Join(dir, ".context", "features", "test-feature")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}

	initial := contextArtifactsFile{SchemaVersion: "1"}
	data, _ := yaml.Marshal(initial)
	if err := os.WriteFile(filepath.Join(featureDir, "artifacts.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}

	p := NewContextArtifactProvider(dir, "test-feature")
	p.MarkStepStart("s1")

	final := contextArtifactsFile{
		SchemaVersion: "1",
		Artifacts: []contextArtifactEntry{
			{ID: "new-1", Type: "image", Path: "s.png", CreatedBy: "miyazaki"},
			{ID: "new-2", Type: "image", Path: "e.png", CreatedBy: "miyazaki"},
			{ID: "new-3", Type: "image", Path: "n.png", CreatedBy: "miyazaki"},
		},
	}
	data, _ = yaml.Marshal(final)
	if err := os.WriteFile(filepath.Join(featureDir, "artifacts.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}

	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(arts) != 3 {
		t.Fatalf("expected 3 new artifacts, got %d: %+v", len(arts), arts)
	}
}

// TestContextArtifactProvider_RejectedEntriesAreSkipped preserves the
// existing semantics: entries with status=rejected are never surfaced,
// even if they are new since the snapshot.
func TestContextArtifactProvider_RejectedEntriesAreSkipped(t *testing.T) {
	dir := t.TempDir()
	featureDir := filepath.Join(dir, ".context", "features", "test-feature")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}

	initial := contextArtifactsFile{SchemaVersion: "1"}
	data, _ := yaml.Marshal(initial)
	if err := os.WriteFile(filepath.Join(featureDir, "artifacts.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}

	p := NewContextArtifactProvider(dir, "test-feature")
	p.MarkStepStart("s1")

	final := contextArtifactsFile{
		SchemaVersion: "1",
		Artifacts: []contextArtifactEntry{
			{ID: "good", Type: "image", Path: "ok.png", CreatedBy: "miyazaki"},
			{ID: "bad", Type: "image", Path: "rejected.png", CreatedBy: "miyazaki", Status: "rejected"},
		},
	}
	data, _ = yaml.Marshal(final)
	if err := os.WriteFile(filepath.Join(featureDir, "artifacts.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}

	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("expected 1 artifact (rejected excluded), got %d: %+v", len(arts), arts)
	}
	if arts[0].Path != "ok.png" {
		t.Errorf("unexpected path: %q", arts[0].Path)
	}
}

// TestContextArtifactProvider_NoSnapshotTimestampFallback preserves
// backward compatibility for callers that don't MarkStepStart but do
// stamp trustworthy created_at values. Without the snapshot, the legacy
// time-based filter is the only signal available.
func TestContextArtifactProvider_NoSnapshotTimestampFallback(t *testing.T) {
	dir := t.TempDir()
	featureDir := filepath.Join(dir, ".context", "features", "test-feature")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a provider but DO NOT call MarkStepStart -- snapshot is nil.
	// Callers relying on timestamp-only filtering should still work when
	// they pre-populate collectMarkers some other way (or when they just
	// use the CreatedBy==stepID path, also tested below).
	//
	// Here we exercise the CreatedBy==stepID legacy path because it
	// doesn't need MarkStepStart at all.
	entries := contextArtifactsFile{
		SchemaVersion: "1",
		Artifacts: []contextArtifactEntry{
			{ID: "a1", Type: "image", Path: "x.png", CreatedBy: "s1"},
		},
	}
	data, _ := yaml.Marshal(entries)
	if err := os.WriteFile(filepath.Join(featureDir, "artifacts.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}

	p := NewContextArtifactProvider(dir, "test-feature")
	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("legacy CreatedBy==stepID fallback should still work, got %d: %+v", len(arts), arts)
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

// TestFilesystemArtifactProvider_IgnoresUnityAndIDENoise pins the ignore
// list that keeps the approval gate uncluttered. Without these filters the
// review drawer surfaces Unity's Library/BurstCache churn, Claude Code's
// `.mcp.json` rewrites, and `.DS_Store` as if they were Miyazaki's
// generated sprites -- rendering the image-gallery with nonsense tiles
// and broken thumbnails.
func TestFilesystemArtifactProvider_IgnoresUnityAndIDENoise(t *testing.T) {
	dir := t.TempDir()
	p := NewFilesystemArtifactProvider(dir)

	// Simulate a Unity project with pre-existing cache dirs. These exist
	// before the step starts; we never want walks to descend into them.
	for _, sub := range []string{
		"Library/BurstCache/Windows-Intel/Hashes/Objects",
		"Temp/Build",
		"Logs",
		".cursor/sessions",
		".claude/cache",
		".vscode",
		"node_modules/some-pkg",
		"obj/Debug",
		"Assets/_Project/Art/Generated/Characters",
	} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatal(err)
		}
	}

	p.MarkStepStart("s1")

	time.Sleep(10 * time.Millisecond)

	// Mutations that should NOT surface as artifacts.
	noise := []string{
		"Library/BurstCache/Windows-Intel/Hashes/Objects/652a1ad4ad",
		"Temp/Build/artifacts.json",
		"Logs/Editor.log",
		".mcp.json",
		".cursor/sessions/ab.json",
		".claude/cache/x.tmp",
		".vscode/settings.json",
		"node_modules/some-pkg/index.js",
		"obj/Debug/tmp.o",
		".DS_Store",
		"Thumbs.db",
	}
	for _, rel := range noise {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// A real agent output -- this one SHOULD surface.
	hero := filepath.Join(dir, "Assets/_Project/Art/Generated/Characters/hero-child-idle-s.png")
	if err := os.WriteFile(hero, []byte("png"), 0644); err != nil {
		t.Fatal(err)
	}

	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("expected exactly 1 artifact (the hero sprite), got %d: %+v", len(arts), arts)
	}
	if filepath.ToSlash(arts[0].Path) != "Assets/_Project/Art/Generated/Characters/hero-child-idle-s.png" {
		t.Errorf("unexpected path: %q", arts[0].Path)
	}
	if arts[0].Kind != "image" {
		t.Errorf("expected kind=image, got %q", arts[0].Kind)
	}
}

// TestFilesystemArtifactProvider_TopLevelNameOnlyMatchesAtRoot guards the
// nuance that top-level ignore names (Library, Temp, obj, ...) should only
// match at the project root. A Unity project could legitimately author an
// `Assets/ThirdParty/Library/` tree; skipping it would silently drop real
// artifacts.
func TestFilesystemArtifactProvider_TopLevelNameOnlyMatchesAtRoot(t *testing.T) {
	dir := t.TempDir()
	p := NewFilesystemArtifactProvider(dir)

	nested := filepath.Join(dir, "Assets", "ThirdParty", "Library")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}

	p.MarkStepStart("s1")
	time.Sleep(10 * time.Millisecond)

	out := filepath.Join(nested, "lib.asset")
	if err := os.WriteFile(out, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("expected 1 artifact from nested Library, got %d: %+v", len(arts), arts)
	}
	if filepath.ToSlash(arts[0].Path) != "Assets/ThirdParty/Library/lib.asset" {
		t.Errorf("unexpected path: %q", arts[0].Path)
	}
}

// TestFilesystemArtifactProvider_AddIgnoreDirsExtensible lets callers
// (orchestrator config, per-project tuning) add project-specific cache
// names without patching this package.
func TestFilesystemArtifactProvider_AddIgnoreDirsExtensible(t *testing.T) {
	dir := t.TempDir()
	p := NewFilesystemArtifactProvider(dir)
	p.AddIgnoreDirs(".gradle")
	p.AddIgnoreFiles("custom.lock")

	if err := os.MkdirAll(filepath.Join(dir, ".gradle", "caches"), 0755); err != nil {
		t.Fatal(err)
	}

	p.MarkStepStart("s1")
	time.Sleep(10 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(dir, ".gradle", "caches", "x.bin"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "custom.lock"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(arts) != 0 {
		t.Fatalf("expected 0 artifacts after extra ignores, got %d: %+v", len(arts), arts)
	}
}

// TestFilesystemArtifactProvider_SnapshotAndCollectUseSameFilter guards an
// easy-to-miss invariant: if MarkStepStart skipped a dir but Collect
// walked into it, the collection walk would find files not in the
// snapshot (`!existed` -> `true`) and report them as new artifacts even
// though they existed before the step ran. Both walks must apply the same
// filter.
func TestFilesystemArtifactProvider_SnapshotAndCollectUseSameFilter(t *testing.T) {
	dir := t.TempDir()
	p := NewFilesystemArtifactProvider(dir)

	// Pre-existing files under ignored dirs -- present before step start.
	if err := os.MkdirAll(filepath.Join(dir, "Library"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Library", "old.cache"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	p.MarkStepStart("s1")
	time.Sleep(10 * time.Millisecond)

	// No new work this step -- the step is a pure no-op.
	arts, err := p.Collect("s1")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(arts) != 0 {
		t.Fatalf("ignored files should not be reported as new artifacts, got %d: %+v", len(arts), arts)
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
