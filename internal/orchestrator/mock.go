package orchestrator

import (
	"sync"
	"time"

	"github.com/rauriemo/anthem/internal/types"
)

type MockEventBus struct {
	mu          sync.Mutex
	subscribers []chan types.Event
	Published   []types.Event
}

func NewMockEventBus() *MockEventBus {
	return &MockEventBus{}
}

func (m *MockEventBus) Publish(event types.Event) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	m.mu.Lock()
	m.Published = append(m.Published, event)
	for _, ch := range m.subscribers {
		select {
		case ch <- event:
		default:
			// drop if full (non-blocking)
		}
	}
	m.mu.Unlock()
}

func (m *MockEventBus) Subscribe() <-chan types.Event {
	ch := make(chan types.Event, 64)
	m.mu.Lock()
	m.subscribers = append(m.subscribers, ch)
	m.mu.Unlock()
	return ch
}
