package backend

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rauriemo/anthem/internal/types"
)

// GitHubLoopBackend implements ExecutionBackend by polling a GitHub issue
// tracker on a fixed interval and delegating each tick to a LoopHost.
type GitHubLoopBackend struct {
	host   LoopHost
	logger *slog.Logger

	mu        sync.Mutex
	callbacks []func(BackendEvent)
	cancel    context.CancelFunc
	done      chan struct{}
}

func NewGitHubLoopBackend(host LoopHost, logger *slog.Logger) *GitHubLoopBackend {
	if logger == nil {
		logger = slog.Default()
	}
	return &GitHubLoopBackend{host: host, logger: logger}
}

func (b *GitHubLoopBackend) Start(ctx context.Context) error {
	ctx, b.cancel = context.WithCancel(ctx)
	b.done = make(chan struct{})

	interval := time.Duration(b.host.PollingIntervalMS()) * time.Millisecond

	go func() {
		defer close(b.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		b.host.Tick(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.host.Tick(ctx)
			}
		}
	}()

	return nil
}

func (b *GitHubLoopBackend) Stop() {
	if b.cancel != nil {
		b.cancel()
	}
	if b.done != nil {
		<-b.done
	}
}

// QueueWork is a no-op for the GitHub loop backend; work is discovered via
// tracker polling, not external injection.
func (b *GitHubLoopBackend) QueueWork(_ []types.Task) error { return nil }

// ActiveWork returns nil; the host (orchestrator) owns the active run tracking.
func (b *GitHubLoopBackend) ActiveWork() []types.Task { return nil }

func (b *GitHubLoopBackend) OnProgress(callback func(BackendEvent)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.callbacks = append(b.callbacks, callback)
}
