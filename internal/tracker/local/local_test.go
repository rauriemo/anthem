package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rauriemo/anthem/internal/types"
)

func seedFile(t *testing.T, path string, tasks []types.Task) {
	t.Helper()
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestListActiveFiltersTerminal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")

	seedFile(t, path, []types.Task{
		{ID: "1", Title: "Queued", Status: types.StatusQueued},
		{ID: "2", Title: "Done", Status: types.StatusCompleted},
		{ID: "3", Title: "Planned", Status: types.StatusPlanned},
	})

	tracker := New(path)
	tasks, err := tracker.ListActive(context.Background())
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("len(tasks) = %d, want 2 (queued + planned)", len(tasks))
	}
}

func TestGetTaskFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	seedFile(t, path, []types.Task{
		{ID: "42", Title: "The Task"},
	})

	tracker := New(path)
	task, err := tracker.GetTask(context.Background(), "42")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task == nil || task.Title != "The Task" {
		t.Errorf("expected task with title 'The Task', got %v", task)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	seedFile(t, path, []types.Task{{ID: "1"}})

	tracker := New(path)
	task, err := tracker.GetTask(context.Background(), "999")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task != nil {
		t.Error("expected nil for unknown ID")
	}
}

func TestUpdateStatusPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	seedFile(t, path, []types.Task{
		{ID: "1", Status: types.StatusQueued},
	})

	tracker := New(path)
	ctx := context.Background()

	if err := tracker.UpdateStatus(ctx, "1", string(types.StatusCompleted)); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	task, _ := tracker.GetTask(ctx, "1")
	if task.Status != types.StatusCompleted {
		t.Errorf("status = %q, want %q", task.Status, types.StatusCompleted)
	}
}

func TestAddRemoveLabel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	seedFile(t, path, []types.Task{
		{ID: "1", Labels: []string{"todo"}},
	})

	tracker := New(path)
	ctx := context.Background()

	if err := tracker.AddLabel(ctx, "1", "in-progress"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	task, _ := tracker.GetTask(ctx, "1")
	if len(task.Labels) != 2 {
		t.Fatalf("expected 2 labels after add, got %d", len(task.Labels))
	}

	if err := tracker.RemoveLabel(ctx, "1", "todo"); err != nil {
		t.Fatalf("RemoveLabel: %v", err)
	}
	task, _ = tracker.GetTask(ctx, "1")
	if len(task.Labels) != 1 || task.Labels[0] != "in-progress" {
		t.Errorf("labels after remove = %v, want [in-progress]", task.Labels)
	}
}

func TestLoadMissingFileReturnsError(t *testing.T) {
	tracker := New("/nonexistent/tasks.json")
	_, err := tracker.ListActive(context.Background())
	if err == nil {
		t.Error("expected error for missing file")
	}
}
