package orchestrator

import (
	"log/slog"
	"sync"
	"time"

	"github.com/rauriemo/anthem/internal/types"
)

const defaultBufferSize = 256

type EventBus interface {
	Publish(event types.Event)
	Subscribe() <-chan types.Event
}

// ChannelEventBus is a non-blocking, fan-out event bus using buffered channels.
// Publish never blocks — if a subscriber's buffer is full, the oldest event is dropped.
type ChannelEventBus struct {
	mu          sync.RWMutex
	subscribers []*subscriber
	logger      *slog.Logger
}

type subscriber struct {
	ch chan types.Event
}

func NewEventBus(logger *slog.Logger) *ChannelEventBus {
	if logger == nil {
		logger = slog.Default()
	}
	return &ChannelEventBus{logger: logger}
}

func (b *ChannelEventBus) Publish(event types.Event) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, sub := range b.subscribers {
		select {
		case sub.ch <- event:
		default:
			// Buffer full — drop oldest, then send
			select {
			case <-sub.ch:
				b.logger.Warn("event bus: dropped oldest event for slow subscriber")
			default:
			}
			select {
			case sub.ch <- event:
			default:
			}
		}
	}
}

func (b *ChannelEventBus) Subscribe() <-chan types.Event {
	ch := make(chan types.Event, defaultBufferSize)
	b.mu.Lock()
	b.subscribers = append(b.subscribers, &subscriber{ch: ch})
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a previously subscribed channel. The channel is closed
// after removal so readers can detect the unsubscribe.
func (b *ChannelEventBus) Unsubscribe(ch <-chan types.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, sub := range b.subscribers {
		if sub.ch == ch {
			close(sub.ch)
			b.subscribers = append(b.subscribers[:i], b.subscribers[i+1:]...)
			return
		}
	}
}
