package config

import (
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const debounceDelay = 100 * time.Millisecond

// Watcher watches a WORKFLOW.md file for changes and triggers hot-reload.
type Watcher struct {
	path     string
	onChange func(*Config, string)
	logger   *slog.Logger
	watcher  *fsnotify.Watcher
	done     chan struct{}
}

func NewWatcher(path string, onChange func(*Config, string), logger *slog.Logger) *Watcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Watcher{
		path:     path,
		onChange: onChange,
		logger:   logger,
		done:     make(chan struct{}),
	}
}

func (w *Watcher) Start() error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.watcher = fw

	// Watch the directory (not the file) so we catch delete+create patterns
	// used by many editors on save.
	dir := filepath.Dir(w.path)
	if err := fw.Add(dir); err != nil {
		fw.Close()
		return err
	}

	go w.loop()
	w.logger.Info("config watcher started", "path", w.path)
	return nil
}

func (w *Watcher) Stop() {
	close(w.done)
	if w.watcher != nil {
		w.watcher.Close()
	}
}

func (w *Watcher) loop() {
	var debounce *time.Timer
	target := filepath.Clean(w.path)

	for {
		select {
		case <-w.done:
			if debounce != nil {
				debounce.Stop()
			}
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			// Only react to our target file
			if filepath.Clean(event.Name) != target {
				continue
			}

			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			// Debounce: reset timer on each event
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(debounceDelay, func() {
				w.reload()
			})

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.logger.Warn("config watcher error", "error", err)
		}
	}
}

func (w *Watcher) reload() {
	cfg, body, err := LoadFile(w.path)
	if err != nil {
		w.logger.Warn("config reload failed, keeping previous", "error", err)
		return
	}

	if err := Validate(cfg); err != nil {
		w.logger.Warn("config reload failed validation, keeping previous", "error", err)
		return
	}

	w.onChange(cfg, body)
	w.logger.Info("config reloaded", "path", w.path)
}
