package local

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/rauriemo/anthem/internal/types"
)

// LocalJSONTracker implements tracker.IssueTracker backed by a JSON file.
type LocalJSONTracker struct {
	mu   sync.Mutex
	path string
}

func New(path string) *LocalJSONTracker {
	return &LocalJSONTracker{path: path}
}

func (l *LocalJSONTracker) load() ([]types.Task, error) {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return nil, fmt.Errorf("reading tasks file: %w", err)
	}
	var tasks []types.Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, fmt.Errorf("parsing tasks file: %w", err)
	}
	return tasks, nil
}

func (l *LocalJSONTracker) save(tasks []types.Task) error {
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling tasks: %w", err)
	}
	return os.WriteFile(l.path, data, 0644)
}

func (l *LocalJSONTracker) ListActive(_ context.Context) ([]types.Task, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	tasks, err := l.load()
	if err != nil {
		return nil, err
	}
	var active []types.Task
	for _, t := range tasks {
		if !t.Status.IsTerminal() {
			active = append(active, t)
		}
	}
	return active, nil
}

func (l *LocalJSONTracker) GetTask(_ context.Context, id string) (*types.Task, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	tasks, err := l.load()
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if tasks[i].ID == id {
			return &tasks[i], nil
		}
	}
	return nil, nil
}

func (l *LocalJSONTracker) UpdateStatus(_ context.Context, id string, status string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	tasks, err := l.load()
	if err != nil {
		return err
	}
	for i := range tasks {
		if tasks[i].ID == id {
			tasks[i].Status = types.Status(status)
			return l.save(tasks)
		}
	}
	return nil
}

func (l *LocalJSONTracker) AddComment(_ context.Context, _ string, _ string) error {
	return nil
}

func (l *LocalJSONTracker) AddLabel(_ context.Context, id string, label string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	tasks, err := l.load()
	if err != nil {
		return err
	}
	for i := range tasks {
		if tasks[i].ID == id {
			tasks[i].Labels = append(tasks[i].Labels, label)
			return l.save(tasks)
		}
	}
	return nil
}

func (l *LocalJSONTracker) RemoveLabel(_ context.Context, id string, label string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	tasks, err := l.load()
	if err != nil {
		return err
	}
	for i := range tasks {
		if tasks[i].ID == id {
			filtered := tasks[i].Labels[:0]
			for _, existing := range tasks[i].Labels {
				if existing != label {
					filtered = append(filtered, existing)
				}
			}
			tasks[i].Labels = filtered
			return l.save(tasks)
		}
	}
	return nil
}

func (l *LocalJSONTracker) CreateIssue(_ context.Context, title string, body string, labels []string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	tasks, err := l.load()
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("%d", len(tasks)+1)
	task := types.Task{
		ID:        id,
		Title:     title,
		Body:      body,
		Labels:    labels,
		Status:    types.StatusQueued,
		CreatedAt: time.Now(),
	}
	tasks = append(tasks, task)
	if err := l.save(tasks); err != nil {
		return "", fmt.Errorf("saving new issue: %w", err)
	}
	return id, nil
}

func (l *LocalJSONTracker) ShouldThrottle() (bool, time.Duration) {
	return false, 0
}
