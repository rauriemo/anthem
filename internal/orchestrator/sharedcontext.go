package orchestrator

import "sync"

// SharedContext is a per-channel in-memory store for long-term session knowledge.
// Updated every round across all modes.
type SharedContext struct {
	mu   sync.Mutex
	docs map[string]string
}

func NewSharedContext() *SharedContext {
	return &SharedContext{
		docs: make(map[string]string),
	}
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
}
