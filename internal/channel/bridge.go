package channel

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/rauriemo/anthem/internal/types"
)

type EventBridge struct {
	manager      *Manager
	eventCh      <-chan types.Event
	allowedTypes map[string]bool
	logger       *slog.Logger
	cancel       context.CancelFunc
}

func NewEventBridge(manager *Manager, eventCh <-chan types.Event, allowedTypes []string, logger *slog.Logger) *EventBridge {
	if logger == nil {
		logger = slog.Default()
	}
	allowed := make(map[string]bool, len(allowedTypes))
	for _, t := range allowedTypes {
		allowed[t] = true
	}
	return &EventBridge{
		manager:      manager,
		eventCh:      eventCh,
		allowedTypes: allowed,
		logger:       logger,
	}
}

func (b *EventBridge) Start(ctx context.Context) {
	ctx, b.cancel = context.WithCancel(ctx)
	go b.run(ctx)
}

func (b *EventBridge) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-b.eventCh:
			if !ok {
				return
			}
			if len(b.allowedTypes) > 0 && !b.allowedTypes[ev.Type] {
				continue
			}
			msg := OutgoingMessage{
				Text:     FormatEvent(ev),
				Markdown: true,
			}
			if err := b.manager.Broadcast(ctx, msg); err != nil {
				b.logger.Warn("event bridge broadcast failed", "event_type", ev.Type, "error", err)
			}
		}
	}
}

func (b *EventBridge) Close() {
	if b.cancel != nil {
		b.cancel()
	}
}

func FormatEvent(event types.Event) string {
	switch event.Type {
	case "task.completed":
		return fmt.Sprintf("Task **%s** completed.", event.TaskID)
	case "task.failed":
		if s, ok := event.Data.(string); ok {
			return fmt.Sprintf("Task **%s** failed: %s", event.TaskID, s)
		}
		return fmt.Sprintf("Task **%s** failed.", event.TaskID)
	case "wave.completed":
		return "Wave completed."
	case "task.waiting_approval":
		return fmt.Sprintf("Task **%s** needs approval.", event.TaskID)
	case "task.budget_exceeded":
		return fmt.Sprintf("Task **%s** exceeded budget.", event.TaskID)
	case "orchestrator.stopped":
		return "Orchestrator shutting down."
	case "maintenance.suggested":
		switch v := event.Data.(type) {
		case string:
			return fmt.Sprintf("Maintenance suggested: %s", v)
		case fmt.Stringer:
			return fmt.Sprintf("Maintenance suggested: %s", v.String())
		default:
			if event.TaskID != "" {
				return fmt.Sprintf("Maintenance suggested for task **%s**.", event.TaskID)
			}
			return "Maintenance suggested."
		}
	default:
		if event.TaskID != "" {
			return fmt.Sprintf("Event: %s (task %s)", event.Type, event.TaskID)
		}
		return fmt.Sprintf("Event: %s", event.Type)
	}
}
