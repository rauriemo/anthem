package workspace

import "sync"

// FileLock provides per-file mutual exclusion for concurrent access
// to shared files like WORKFLOW.md and VOICE.md.
type FileLock struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewFileLock() *FileLock {
	return &FileLock{locks: make(map[string]*sync.Mutex)}
}

func (fl *FileLock) Lock(path string) {
	fl.mu.Lock()
	lock, ok := fl.locks[path]
	if !ok {
		lock = &sync.Mutex{}
		fl.locks[path] = lock
	}
	fl.mu.Unlock()
	lock.Lock()
}

func (fl *FileLock) Unlock(path string) {
	fl.mu.Lock()
	lock := fl.locks[path]
	fl.mu.Unlock()
	if lock != nil {
		lock.Unlock()
	}
}
