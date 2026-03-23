package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/types"
)

func TestFileLockConcurrent(t *testing.T) {
	fl := NewFileLock()
	counter := 0
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fl.Lock("shared.txt")
			counter++
			fl.Unlock("shared.txt")
		}()
	}
	wg.Wait()

	if counter != 100 {
		t.Errorf("counter = %d, want 100 (race detected)", counter)
	}
}

func TestFileLockDifferentPaths(t *testing.T) {
	fl := NewFileLock()
	done := make(chan struct{}, 2)

	fl.Lock("a.txt")
	go func() {
		fl.Lock("b.txt")
		fl.Unlock("b.txt")
		done <- struct{}{}
	}()

	<-done
	fl.Unlock("a.txt")
}

func TestValidatePathAcceptsSubpath(t *testing.T) {
	if err := ValidatePath("/workspace", "/workspace/project/file.go"); err != nil {
		t.Errorf("expected nil for valid subpath, got %v", err)
	}
}

func TestValidatePathRejectsTraversal(t *testing.T) {
	err := ValidatePath("/workspace", "/workspace/../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestValidatePathAcceptsSameDir(t *testing.T) {
	if err := ValidatePath("/workspace", "/workspace"); err != nil {
		t.Errorf("expected nil for same dir, got %v", err)
	}
}

func TestManagerPrepareCreatesDirectory(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, config.HooksConfig{}, nil)

	task := types.Task{ID: "task-42", Identifier: "GH-42", Title: "Test"}
	wsPath, err := mgr.Prepare(context.Background(), task)
	if err != nil {
		t.Fatalf("Prepare() error: %v", err)
	}

	info, err := os.Stat(wsPath)
	if err != nil {
		t.Fatalf("workspace dir does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("workspace path is not a directory")
	}

	wantSuffix := filepath.Join(root, "task-42")
	absWant, _ := filepath.Abs(wantSuffix)
	if wsPath != absWant {
		t.Errorf("Prepare() = %q, want %q", wsPath, absWant)
	}
}

func TestManagerPrepareRunsAfterCreateHook(t *testing.T) {
	root := t.TempDir()

	// Use a Go helper binary approach: write a small Go program? No — just use echo.
	// On Windows, cmd /C "echo ok > file" works if we avoid quotes in the path.
	// Since cmd.Dir is the workspace, we can write to a relative path.
	var hookCmd string
	if runtime.GOOS == "windows" {
		hookCmd = `echo ok > hook-ran.txt`
	} else {
		hookCmd = `echo ok > hook-ran.txt`
	}

	mgr := NewManager(root, config.HooksConfig{AfterCreate: hookCmd}, nil)

	task := types.Task{ID: "task-hook", Identifier: "GH-1"}
	wsPath, err := mgr.Prepare(context.Background(), task)
	if err != nil {
		t.Fatalf("Prepare() error: %v", err)
	}

	markerFile := filepath.Join(wsPath, "hook-ran.txt")
	if _, err := os.Stat(markerFile); os.IsNotExist(err) {
		t.Error("after_create hook did not run: marker file not found")
	}
}

func TestManagerPrepareFailsOnBadAfterCreateHook(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, config.HooksConfig{AfterCreate: "false"}, nil)

	task := types.Task{ID: "task-fail", Identifier: "GH-2"}
	_, err := mgr.Prepare(context.Background(), task)
	if err == nil {
		t.Error("expected error when after_create hook fails")
	}
}

func TestManagerRunHookBeforeRunRetries(t *testing.T) {
	// Test that before_run retries and eventually fails after max attempts
	// by using a command that always fails.
	root := t.TempDir()

	// "false" always exits non-zero on both Windows (via cmd /C) and Unix
	mgr := NewManager(root, config.HooksConfig{BeforeRun: "exit 1"}, nil)

	err := mgr.RunHook(context.Background(), "before_run", root)
	if err == nil {
		t.Fatal("RunHook(before_run) should return error after all retries exhausted")
	}
	if !strings.Contains(err.Error(), "failed after 3 attempts") {
		t.Errorf("error should mention retry count, got: %v", err)
	}
}

func TestManagerRunHookBeforeRunSucceedsOnFirstTry(t *testing.T) {
	root := t.TempDir()

	var hookCmd string
	if runtime.GOOS == "windows" {
		hookCmd = "echo ok > hook-ok.txt"
	} else {
		hookCmd = "echo ok > hook-ok.txt"
	}

	mgr := NewManager(root, config.HooksConfig{BeforeRun: hookCmd}, nil)

	err := mgr.RunHook(context.Background(), "before_run", root)
	if err != nil {
		t.Fatalf("RunHook(before_run) should succeed on first try, got: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "hook-ok.txt")); os.IsNotExist(err) {
		t.Error("expected hook to create marker file")
	}
}

func TestManagerRunHookAfterCompleteWarnOnly(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, config.HooksConfig{AfterComplete: "false"}, nil)

	err := mgr.RunHook(context.Background(), "after_complete", root)
	if err != nil {
		t.Errorf("RunHook(after_complete) should not return error on failure, got: %v", err)
	}
}

func TestManagerCleanupRemovesDirectory(t *testing.T) {
	root := t.TempDir()
	taskDir := filepath.Join(root, "task-99")
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Put a file in it to confirm recursive removal
	if err := os.WriteFile(filepath.Join(taskDir, "file.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(root, config.HooksConfig{}, nil)
	if err := mgr.Cleanup(context.Background(), "task-99"); err != nil {
		t.Fatalf("Cleanup() error: %v", err)
	}

	if _, err := os.Stat(taskDir); !os.IsNotExist(err) {
		t.Error("expected workspace directory to be removed after Cleanup")
	}
}

func TestManagerPrepareRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, config.HooksConfig{}, nil)

	task := types.Task{ID: "../escape", Identifier: "GH-99"}
	_, err := mgr.Prepare(context.Background(), task)
	if err == nil {
		t.Error("expected error for path traversal task ID")
	}
}

func TestManagerCleanupRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, config.HooksConfig{}, nil)

	err := mgr.Cleanup(context.Background(), "../escape")
	if err == nil {
		t.Error("expected error for path traversal task ID in Cleanup")
	}
}

func TestManagerCleanupTerminal(t *testing.T) {
	root := t.TempDir()

	// Create some workspace dirs
	for _, id := range []string{"task-1", "task-2", "task-3"} {
		if err := os.MkdirAll(filepath.Join(root, id), 0755); err != nil {
			t.Fatal(err)
		}
	}

	mgr := NewManager(root, config.HooksConfig{}, nil)
	mgr.CleanupTerminal([]string{"task-1", "task-3"})

	// task-1 and task-3 should be removed
	if _, err := os.Stat(filepath.Join(root, "task-1")); !os.IsNotExist(err) {
		t.Error("expected task-1 directory to be removed")
	}
	if _, err := os.Stat(filepath.Join(root, "task-3")); !os.IsNotExist(err) {
		t.Error("expected task-3 directory to be removed")
	}
	// task-2 should still exist
	if _, err := os.Stat(filepath.Join(root, "task-2")); err != nil {
		t.Error("expected task-2 directory to still exist")
	}
}

func TestManagerRunHookNoOp(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, config.HooksConfig{}, nil)

	// Running a hook with no command configured should be a no-op
	err := mgr.RunHook(context.Background(), "before_run", root)
	if err != nil {
		t.Errorf("RunHook with no command should return nil, got: %v", err)
	}
}
