package config

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const validWorkflow = `---
tracker:
  kind: github
  repo: "test/repo"
  labels:
    active: ["todo"]
    terminal: ["done"]
polling:
  interval_ms: 10000
workspace:
  root: "./workspaces"
agent:
  command: "claude"
  max_turns: 5
  max_concurrent: 3
  stall_timeout_ms: 300000
  max_retry_backoff_ms: 300000
---
Work on {{.issue.title}}
`

const validWorkflowUpdated = `---
tracker:
  kind: github
  repo: "test/repo"
  labels:
    active: ["todo"]
    terminal: ["done"]
polling:
  interval_ms: 20000
workspace:
  root: "./workspaces"
agent:
  command: "claude"
  max_turns: 10
  max_concurrent: 3
  stall_timeout_ms: 300000
  max_retry_backoff_ms: 300000
---
Work on {{.issue.title}} updated
`

const invalidWorkflow = `---
tracker:
  kind: "invalid_tracker"
  repo: ""
---
body
`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestWatcherTriggersOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	writeFile(t, path, validWorkflow)

	var mu sync.Mutex
	var received *Config
	var receivedBody string

	w := NewWatcher(path, func(cfg *Config, body string) {
		mu.Lock()
		received = cfg
		receivedBody = body
		mu.Unlock()
	}, nil)

	if err := w.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer w.Stop()

	// Modify the file
	time.Sleep(50 * time.Millisecond)
	writeFile(t, path, validWorkflowUpdated)

	// Wait for debounce + processing
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	cfg := received
	body := receivedBody
	mu.Unlock()

	if cfg == nil {
		t.Fatal("onChange was not called after file modification")
	}
	if cfg.Polling.IntervalMS != 20000 {
		t.Errorf("IntervalMS = %d, want 20000", cfg.Polling.IntervalMS)
	}
	if cfg.Agent.MaxTurns != 10 {
		t.Errorf("MaxTurns = %d, want 10", cfg.Agent.MaxTurns)
	}
	if body != "Work on {{.issue.title}} updated" {
		t.Errorf("body = %q, want updated template", body)
	}
}

func TestWatcherInvalidConfigKeepsPrevious(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	writeFile(t, path, validWorkflow)

	var callCount atomic.Int32

	w := NewWatcher(path, func(_ *Config, _ string) {
		callCount.Add(1)
	}, nil)

	if err := w.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer w.Stop()

	// Write invalid config
	time.Sleep(50 * time.Millisecond)
	writeFile(t, path, invalidWorkflow)

	// Wait for debounce
	time.Sleep(500 * time.Millisecond)

	if callCount.Load() != 0 {
		t.Errorf("onChange should not be called for invalid config, got %d calls", callCount.Load())
	}
}

func TestWatcherDebounce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	writeFile(t, path, validWorkflow)

	var callCount atomic.Int32

	w := NewWatcher(path, func(_ *Config, _ string) {
		callCount.Add(1)
	}, nil)

	if err := w.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer w.Stop()

	// Rapid writes
	time.Sleep(50 * time.Millisecond)
	for i := 0; i < 5; i++ {
		writeFile(t, path, validWorkflowUpdated)
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for debounce to settle
	time.Sleep(500 * time.Millisecond)

	count := callCount.Load()
	if count != 1 {
		t.Errorf("onChange called %d times, want 1 (debounce should coalesce rapid writes)", count)
	}
}

func TestWatcherStopCleanly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	writeFile(t, path, validWorkflow)

	w := NewWatcher(path, func(_ *Config, _ string) {}, nil)

	if err := w.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Stop should not panic or block
	w.Stop()

	// Writes after stop should not panic
	writeFile(t, path, validWorkflowUpdated)
	time.Sleep(200 * time.Millisecond)
}

func TestWatcherDeleteAndRecreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	writeFile(t, path, validWorkflow)

	var mu sync.Mutex
	var received *Config

	w := NewWatcher(path, func(cfg *Config, _ string) {
		mu.Lock()
		received = cfg
		mu.Unlock()
	}, nil)

	if err := w.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer w.Stop()

	// Simulate editor save: delete + recreate
	time.Sleep(50 * time.Millisecond)
	os.Remove(path)
	time.Sleep(20 * time.Millisecond)
	writeFile(t, path, validWorkflowUpdated)

	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	cfg := received
	mu.Unlock()

	if cfg == nil {
		t.Fatal("onChange was not called after delete+recreate")
	}
	if cfg.Agent.MaxTurns != 10 {
		t.Errorf("MaxTurns = %d, want 10", cfg.Agent.MaxTurns)
	}
}
