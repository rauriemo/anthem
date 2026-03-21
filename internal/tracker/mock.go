package tracker

import (
	"context"
	"sync"

	"github.com/rauriemo/anthem/internal/types"
)

type MockTracker struct {
	mu       sync.Mutex
	Tasks    []types.Task
	Comments map[string][]string // task ID -> comments
}

func NewMockTracker(tasks []types.Task) *MockTracker {
	return &MockTracker{
		Tasks:    tasks,
		Comments: make(map[string][]string),
	}
}

func (m *MockTracker) ListActive(_ context.Context) ([]types.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var active []types.Task
	for _, t := range m.Tasks {
		if !t.Status.IsTerminal() {
			active = append(active, t)
		}
	}
	return active, nil
}

func (m *MockTracker) GetTask(_ context.Context, id string) (*types.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.Tasks {
		if m.Tasks[i].ID == id {
			return &m.Tasks[i], nil
		}
	}
	return nil, nil
}

func (m *MockTracker) UpdateStatus(_ context.Context, id string, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.Tasks {
		if m.Tasks[i].ID == id {
			m.Tasks[i].Status = types.Status(status)
			return nil
		}
	}
	return nil
}

func (m *MockTracker) AddComment(_ context.Context, id string, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Comments[id] = append(m.Comments[id], body)
	return nil
}

func (m *MockTracker) AddLabel(_ context.Context, id string, label string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.Tasks {
		if m.Tasks[i].ID == id {
			m.Tasks[i].Labels = append(m.Tasks[i].Labels, label)
			return nil
		}
	}
	return nil
}

func (m *MockTracker) RemoveLabel(_ context.Context, id string, label string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.Tasks {
		if m.Tasks[i].ID == id {
			filtered := m.Tasks[i].Labels[:0]
			for _, l := range m.Tasks[i].Labels {
				if l != label {
					filtered = append(filtered, l)
				}
			}
			m.Tasks[i].Labels = filtered
			return nil
		}
	}
	return nil
}
