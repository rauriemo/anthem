package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

type Manager struct {
	channels []Channel
	merged   chan IncomingMessage
	logger   *slog.Logger
	mu       sync.Mutex
}

func NewManager(logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		merged: make(chan IncomingMessage, 64),
		logger: logger,
	}
}

func (m *Manager) Register(ch Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels = append(m.channels, ch)
}

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	channels := make([]Channel, len(m.channels))
	copy(channels, m.channels)
	m.mu.Unlock()

	var errs []error
	for _, ch := range channels {
		if err := ch.Start(ctx); err != nil {
			errs = append(errs, fmt.Errorf("starting channel %s: %w", ch.Kind(), err))
			continue
		}
		go m.fanIn(ctx, ch)
	}
	return errors.Join(errs...)
}

func (m *Manager) fanIn(ctx context.Context, ch Channel) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch.Incoming():
			if !ok {
				return
			}
			select {
			case m.merged <- msg:
			default:
				m.logger.Warn("dropping incoming message, merged channel full", "channel", ch.Kind())
			}
		}
	}
}

func (m *Manager) Broadcast(ctx context.Context, msg OutgoingMessage) error {
	m.mu.Lock()
	channels := make([]Channel, len(m.channels))
	copy(channels, m.channels)
	m.mu.Unlock()

	var errs []error
	for _, ch := range channels {
		if err := ch.Send(ctx, msg); err != nil {
			errs = append(errs, fmt.Errorf("sending to channel %s: %w", ch.Kind(), err))
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) Incoming() <-chan IncomingMessage {
	return m.merged
}

func (m *Manager) Close() error {
	m.mu.Lock()
	channels := make([]Channel, len(m.channels))
	copy(channels, m.channels)
	m.mu.Unlock()

	var errs []error
	for _, ch := range channels {
		if err := ch.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing channel %s: %w", ch.Kind(), err))
		}
	}
	return errors.Join(errs...)
}
