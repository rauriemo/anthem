package orchestrator

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSharedContext_GetEmpty(t *testing.T) {
	sc := NewSharedContext()
	if got := sc.Get("missing"); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestSharedContext_UpdateAndGet(t *testing.T) {
	sc := NewSharedContext()
	sc.Update("prism", "Key decisions: use card system with progression")
	if got := sc.Get("prism"); got != "Key decisions: use card system with progression" {
		t.Errorf("unexpected value: %q", got)
	}
}

func TestSharedContext_Overwrite(t *testing.T) {
	sc := NewSharedContext()
	sc.Update("prism", "original")
	sc.Update("prism", "updated")
	if got := sc.Get("prism"); got != "updated" {
		t.Errorf("expected 'updated', got %q", got)
	}
}

func TestSharedContext_MultipleChannels(t *testing.T) {
	sc := NewSharedContext()
	sc.Update("prism", "prism context")
	sc.Update("slack", "slack context")

	if got := sc.Get("prism"); got != "prism context" {
		t.Errorf("prism context wrong: %q", got)
	}
	if got := sc.Get("slack"); got != "slack context" {
		t.Errorf("slack context wrong: %q", got)
	}
}

func TestSharedContext_PersistAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-context.yaml")

	sc1, err := NewSharedContextWithFile(path)
	if err != nil {
		t.Fatalf("NewSharedContextWithFile: %v", err)
	}
	sc1.Update("channel-a", "persisted body")

	sc2, err := NewSharedContextWithFile(path)
	if err != nil {
		t.Fatalf("reload NewSharedContextWithFile: %v", err)
	}
	if got := sc2.Get("channel-a"); got != "persisted body" {
		t.Errorf("after reload Get: want %q, got %q", "persisted body", got)
	}
}

func TestSharedContext_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ctx.yaml")

	sc, err := NewSharedContextWithFile(path)
	if err != nil {
		t.Fatalf("NewSharedContextWithFile: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "k" + strconv.Itoa(i)
			sc.Update(key, "v"+strconv.Itoa(i))
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var parsed map[string]string
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("YAML unmarshal: %v", err)
	}
	if len(parsed) != 20 {
		t.Errorf("want 20 keys in file, got %d", len(parsed))
	}
}

func TestSharedContext_MissingFileOnStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.yaml")

	sc, err := NewSharedContextWithFile(path)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if got := sc.Get("any"); got != "" {
		t.Errorf("empty start: Get want empty, got %q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected no file at %q before Update", path)
	}
}

func TestSharedContext_Snapshot(t *testing.T) {
	sc := NewSharedContext()
	sc.Update("a", "one")
	sc.Update("b", "two")

	snap := sc.Snapshot()
	snap["a"] = "mutated"
	snap["c"] = "extra"

	if got := sc.Get("a"); got != "one" {
		t.Errorf("Get(a) after mutating snapshot: want %q, got %q", "one", got)
	}
	if got := sc.Get("c"); got != "" {
		t.Errorf("Get(c) should be empty, got %q", got)
	}
}

func TestSharedContext_InMemoryOnly(t *testing.T) {
	dir := t.TempDir()
	sc := NewSharedContext()
	sc.Update("x", "y")
	if got := sc.Get("x"); got != "y" {
		t.Errorf("Get: want %q, got %q", "y", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("in-memory only: want empty temp dir, got %d entries", len(entries))
	}
}
