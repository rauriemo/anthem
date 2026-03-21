package config

// Watcher watches WORKFLOW.md for changes and triggers hot-reload.
// Stub for Phase 2 -- will use fsnotify.
type Watcher struct {
	path     string
	onChange func(*Config, string)
}

func NewWatcher(path string, onChange func(*Config, string)) *Watcher {
	return &Watcher{path: path, onChange: onChange}
}
