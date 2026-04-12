package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// SharedContext is a per-channel in-memory store for long-term session knowledge.
// Optionally file-backed at .context/session-context.yaml.
type SharedContext struct {
	mu       sync.Mutex
	docs     map[string]string
	filePath string
}

// NewSharedContext creates a new in-memory SharedContext.
func NewSharedContext() *SharedContext {
	return &SharedContext{
		docs: make(map[string]string),
	}
}

// NewSharedContextWithFile creates a SharedContext that persists to disk.
// It loads existing data from filePath if the file exists.
func NewSharedContextWithFile(filePath string) (*SharedContext, error) {
	sc := &SharedContext{
		docs:     make(map[string]string),
		filePath: filePath,
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return sc, nil
		}
		return nil, fmt.Errorf("loading shared context: %w", err)
	}
	if err := yaml.Unmarshal(data, &sc.docs); err != nil {
		return nil, fmt.Errorf("parsing shared context: %w", err)
	}
	return sc, nil
}

func (sc *SharedContext) Get(key string) string {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.docs[key]
}

func (sc *SharedContext) Update(key, content string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.docs[key] = content
	if sc.filePath != "" {
		sc.persistLocked()
	}
}

func (sc *SharedContext) persistLocked() {
	out, err := yaml.Marshal(sc.docs)
	if err != nil {
		return
	}
	dir := filepath.Dir(sc.filePath)
	_ = os.MkdirAll(dir, 0755)
	tmp, err := os.CreateTemp(dir, ".tmp-sc-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return
	}
	tmp.Close()
	if err := os.Rename(tmpName, sc.filePath); err != nil {
		os.Remove(tmpName)
	}
}

// Snapshot returns a copy of all entries for inspection.
func (sc *SharedContext) Snapshot() map[string]string {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	out := make(map[string]string, len(sc.docs))
	for k, v := range sc.docs {
		out[k] = v
	}
	return out
}
