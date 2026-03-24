package channel

import (
	"context"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/types"
)

func sendEvent(ch chan<- types.Event, ev types.Event) {
	ch <- ev
}

func TestEventBridge_ForwardsAllowed(t *testing.T) {
	mock := newMockChannel("test")
	mgr := NewManager(nil)
	mgr.Register(mock)

	eventCh := make(chan types.Event, 8)
	bridge := NewEventBridge(mgr, eventCh, []string{"task.completed"}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridge.Start(ctx)

	sendEvent(eventCh, types.Event{Type: "task.completed", TaskID: "1"})
	time.Sleep(50 * time.Millisecond)

	sent := mock.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sent))
	}
	if sent[0].Text != "Task **1** completed." {
		t.Errorf("text = %q, want %q", sent[0].Text, "Task **1** completed.")
	}
	if !sent[0].Markdown {
		t.Error("expected Markdown = true")
	}
}

func TestEventBridge_FiltersDisallowed(t *testing.T) {
	mock := newMockChannel("test")
	mgr := NewManager(nil)
	mgr.Register(mock)

	eventCh := make(chan types.Event, 8)
	bridge := NewEventBridge(mgr, eventCh, []string{"task.completed"}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridge.Start(ctx)

	sendEvent(eventCh, types.Event{Type: "task.failed", TaskID: "2"})
	time.Sleep(50 * time.Millisecond)

	sent := mock.sentMessages()
	if len(sent) != 0 {
		t.Errorf("expected 0 messages for filtered event, got %d", len(sent))
	}
}

func TestEventBridge_EmptyAllowAll(t *testing.T) {
	mock := newMockChannel("test")
	mgr := NewManager(nil)
	mgr.Register(mock)

	eventCh := make(chan types.Event, 8)
	bridge := NewEventBridge(mgr, eventCh, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridge.Start(ctx)

	sendEvent(eventCh, types.Event{Type: "task.completed", TaskID: "1"})
	sendEvent(eventCh, types.Event{Type: "task.failed", TaskID: "2", Data: "crash"})
	sendEvent(eventCh, types.Event{Type: "orchestrator.stopped"})
	time.Sleep(50 * time.Millisecond)

	sent := mock.sentMessages()
	if len(sent) != 3 {
		t.Fatalf("expected 3 messages with empty allowedTypes, got %d", len(sent))
	}
}

func TestFormatEvent(t *testing.T) {
	tests := []struct {
		name  string
		event types.Event
		want  string
	}{
		{"completed", types.Event{Type: "task.completed", TaskID: "42"}, "Task **42** completed."},
		{"failed with data", types.Event{Type: "task.failed", TaskID: "7", Data: "timeout"}, "Task **7** failed: timeout"},
		{"failed no data", types.Event{Type: "task.failed", TaskID: "7"}, "Task **7** failed."},
		{"wave completed", types.Event{Type: "wave.completed"}, "Wave completed."},
		{"waiting approval", types.Event{Type: "task.waiting_approval", TaskID: "3"}, "Task **3** needs approval."},
		{"budget exceeded", types.Event{Type: "task.budget_exceeded", TaskID: "5"}, "Task **5** exceeded budget."},
		{"stopped", types.Event{Type: "orchestrator.stopped"}, "Orchestrator shutting down."},
		{"maintenance with data", types.Event{Type: "maintenance.suggested", Data: "stale tasks"}, "Maintenance suggested: stale tasks"},
		{"maintenance no data", types.Event{Type: "maintenance.suggested"}, "Maintenance suggested."},
		{"unknown with task", types.Event{Type: "custom.event", TaskID: "9"}, "Event: custom.event (task 9)"},
		{"unknown no task", types.Event{Type: "custom.event"}, "Event: custom.event"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatEvent(tt.event)
			if got != tt.want {
				t.Errorf("FormatEvent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEventBridge_Close(t *testing.T) {
	mock := newMockChannel("test")
	mgr := NewManager(nil)
	mgr.Register(mock)

	eventCh := make(chan types.Event, 8)
	bridge := NewEventBridge(mgr, eventCh, nil, nil)

	ctx := context.Background()
	bridge.Start(ctx)
	bridge.Close()

	// Send after close -- should not panic or deliver
	time.Sleep(20 * time.Millisecond)
	select {
	case eventCh <- types.Event{Type: "task.completed", TaskID: "late"}:
	default:
	}
	time.Sleep(20 * time.Millisecond)

	sent := mock.sentMessages()
	if len(sent) != 0 {
		t.Errorf("expected no messages after Close, got %d", len(sent))
	}
}
