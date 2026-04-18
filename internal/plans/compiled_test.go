package plans

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// marshalCompiledBody builds a minimal JSON body that mirrors the
// ExecutionPlan shape enough for LoadCompiled's probe unmarshal to work.
func marshalCompiledBody(t *testing.T, gen int) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"steps": []any{},
		"metadata": map[string]any{
			"plan_generation": gen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestSaveCompiled_HappyPath(t *testing.T) {
	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.Save("owner/repo", "Compile Target", "body")
	if err != nil {
		t.Fatal(err)
	}

	gen1, err := store.SaveCompiled(path, marshalCompiledBody(t, 1), []string{"artist", "animator"})
	if err != nil {
		t.Fatalf("first SaveCompiled: %v", err)
	}
	if gen1 != 1 {
		t.Errorf("first gen = %d, want 1", gen1)
	}

	p, err := store.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Frontmatter.CompileGeneration != 1 {
		t.Errorf("frontmatter gen = %d, want 1", p.Frontmatter.CompileGeneration)
	}
	if len(p.Frontmatter.RequiredProfiles) != 2 {
		t.Errorf("required profiles not snapshotted: got %v", p.Frontmatter.RequiredProfiles)
	}
	if p.Frontmatter.CompiledAt.IsZero() {
		t.Error("CompiledAt not set")
	}

	body, embedded, err := store.LoadCompiled(path)
	if err != nil {
		t.Fatalf("LoadCompiled: %v", err)
	}
	if embedded != 1 {
		t.Errorf("embedded gen = %d, want 1", embedded)
	}
	if len(body) == 0 {
		t.Error("expected non-empty body")
	}

	gen2, err := store.SaveCompiled(path, marshalCompiledBody(t, 2), []string{"artist"})
	if err != nil {
		t.Fatalf("second SaveCompiled: %v", err)
	}
	if gen2 != 2 {
		t.Errorf("second gen = %d, want 2", gen2)
	}
}

func TestLoadCompiled_MissingSidecar(t *testing.T) {
	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.Save("owner/repo", "No Compile Yet", "body")
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = store.LoadCompiled(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist for never-compiled plan, got %v", err)
	}
}

func TestLoadCompiled_DetectsRolledBackFrontmatter(t *testing.T) {
	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.Save("owner/repo", "Drift", "body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveCompiled(path, marshalCompiledBody(t, 1), nil); err != nil {
		t.Fatal(err)
	}

	// Simulate a rollback: rewrite the markdown with CompileGeneration=0.
	p, err := store.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p.Frontmatter.CompileGeneration = 0
	if err := writePlan(path, p.Frontmatter, p.Body); err != nil {
		t.Fatal(err)
	}

	body, embedded, err := store.LoadCompiled(path)
	if !errors.Is(err, ErrCompiledSidecarDrift) {
		t.Errorf("expected ErrCompiledSidecarDrift, got %v", err)
	}
	if embedded != 1 {
		t.Errorf("embedded gen from sidecar = %d, want 1", embedded)
	}
	if len(body) == 0 {
		t.Error("expected body still returned on drift so callers can diagnose")
	}
}

func TestSaveCompiled_FrontmatterFailureAtomicity(t *testing.T) {
	// Make the plan file read-only by making its parent directory read-only
	// after the plan is written, so the SaveCompiled's writePlan step fails.
	// This only works on filesystems that honor mode 0o555 on directories.
	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.Save("owner/repo", "FM Fail", "body")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.SaveCompiled(path, marshalCompiledBody(t, 1), nil)
	if err != nil {
		t.Fatalf("setup SaveCompiled: %v", err)
	}
	// Snapshot current gen after a successful first compile.
	before, err := store.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	// Point the store at a non-existent plan path to force the internal Load
	// in SaveCompiled to fail, exercising the tmp-removed-on-fm-fail branch.
	bogus := filepath.Join(filepath.Dir(path), "does-not-exist.md")
	_, err = store.SaveCompiled(bogus, marshalCompiledBody(t, 2), nil)
	if err == nil {
		t.Fatal("expected SaveCompiled against missing plan to fail")
	}
	if _, statErr := os.Stat(bogus + ".execute.json.tmp"); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("expected tmp sidecar to be absent when Load fails, got statErr=%v", statErr)
	}

	// Real-plan state must be unchanged after the bogus attempt.
	after, err := store.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Frontmatter.CompileGeneration != before.Frontmatter.CompileGeneration {
		t.Errorf("real plan generation changed: before=%d after=%d",
			before.Frontmatter.CompileGeneration, after.Frontmatter.CompileGeneration)
	}
}

func TestSaveCompiled_RenameFailureProducesDrift(t *testing.T) {
	// Simulate the rename-failure case by manually reproducing its post-state
	// (frontmatter bumped, sidecar still at older gen) and asserting that
	// LoadCompiled surfaces ErrCompiledSidecarDrift. A true rename failure
	// from rename(2) is hard to force portably, so the state-equivalent test
	// below is what the plan actually cares about.
	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.Save("owner/repo", "Rename Drift", "body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveCompiled(path, marshalCompiledBody(t, 1), nil); err != nil {
		t.Fatal(err)
	}

	// Bump frontmatter to gen=2 WITHOUT rewriting the sidecar, which is
	// exactly the post-state of a rename failure (gen=N+1 in FM, gen=N on
	// disk).
	p, err := store.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p.Frontmatter.CompileGeneration = 2
	if err := writePlan(path, p.Frontmatter, p.Body); err != nil {
		t.Fatal(err)
	}

	body, embedded, err := store.LoadCompiled(path)
	if !errors.Is(err, ErrCompiledSidecarDrift) {
		t.Errorf("expected drift error, got %v", err)
	}
	if embedded != 1 {
		t.Errorf("embedded gen = %d, want 1 (old sidecar)", embedded)
	}
	if len(body) == 0 {
		t.Error("expected body returned so caller can diagnose")
	}
}

func TestBackfillPreservesFrontmatterCompileFields(t *testing.T) {
	// After backfill of ID, any existing compile fields must be preserved.
	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".anthem", "plans", "owner-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(dir, "with-compile-fields.md")
	content := "---\nproject: owner/repo\nstatus: draft\ntitle: With Fields\ncreated: 2024-01-01T00:00:00Z\nupdated: 2024-01-01T00:00:00Z\ncompile_generation: 3\nrequired_profiles:\n  - artist\n  - animator\n---\n\n# body\n"
	if err := os.WriteFile(planPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := store.Load(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if p.Frontmatter.CompileGeneration != 3 {
		t.Errorf("CompileGeneration = %d, want 3", p.Frontmatter.CompileGeneration)
	}
	if len(p.Frontmatter.RequiredProfiles) != 2 {
		t.Errorf("RequiredProfiles = %v, want 2 entries", p.Frontmatter.RequiredProfiles)
	}
	if p.Frontmatter.ID == "" {
		t.Error("expected ID backfill to still happen")
	}
}
