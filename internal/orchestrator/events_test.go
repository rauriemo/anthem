package orchestrator

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/types"
)

func TestChannelEventBus_PublishSubscribe(t *testing.T) {
	bus := NewEventBus(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	ch := bus.Subscribe()

	bus.Publish(types.Event{Type: "test.event", TaskID: "1"})

	select {
	case event := <-ch:
		if event.Type != "test.event" {
			t.Errorf("type = %q", event.Type)
		}
		if event.TaskID != "1" {
			t.Errorf("task_id = %q", event.TaskID)
		}
		if event.Timestamp.IsZero() {
			t.Error("timestamp should be set")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestChannelEventBus_MultipleSubscribers(t *testing.T) {
	bus := NewEventBus(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	ch1 := bus.Subscribe()
	ch2 := bus.Subscribe()

	bus.Publish(types.Event{Type: "broadcast"})

	for _, ch := range []<-chan types.Event{ch1, ch2} {
		select {
		case event := <-ch:
			if event.Type != "broadcast" {
				t.Errorf("type = %q", event.Type)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout")
		}
	}
}

func TestChannelEventBus_NonBlocking(t *testing.T) {
	bus := NewEventBus(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	_ = bus.Subscribe() // subscribe but never drain

	// Publish more events than the buffer size — must not block
	done := make(chan struct{})
	go func() {
		for i := 0; i < defaultBufferSize+100; i++ {
			bus.Publish(types.Event{Type: "flood"})
		}
		close(done)
	}()

	select {
	case <-done:
		// good — publish didn't block
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on full subscriber buffer")
	}
}
