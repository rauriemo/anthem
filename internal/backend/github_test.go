package backend

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type mockLoopHost struct {
	tickCount  atomic.Int64
	intervalMS int
}

func (m *mockLoopHost) Tick(_ context.Context) {
	m.tickCount.Add(1)
}

func (m *mockLoopHost) PollingIntervalMS() int {
	return m.intervalMS
}

func TestGitHubLoopBackend_StartStop(t *testing.T) {
	host := &mockLoopHost{intervalMS: 50}
	b := NewGitHubLoopBackend(host, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for at least 2 ticks (first immediate + one on interval)
	time.Sleep(120 * time.Millisecond)
	cancel()
	b.Stop()

	ticks := host.tickCount.Load()
	if ticks < 2 {
		t.Errorf("tick count = %d, want >= 2", ticks)
	}
}

func TestGitHubLoopBackend_FirstTickImmediate(t *testing.T) {
	host := &mockLoopHost{intervalMS: 10_000}
	b := NewGitHubLoopBackend(host, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// First tick should happen immediately, not after 10s
	time.Sleep(50 * time.Millisecond)
	cancel()
	b.Stop()

	ticks := host.tickCount.Load()
	if ticks != 1 {
		t.Errorf("tick count = %d, want 1 (immediate tick only, interval is 10s)", ticks)
	}
}

func TestGitHubLoopBackend_StopIdempotent(t *testing.T) {
	host := &mockLoopHost{intervalMS: 100}
	b := NewGitHubLoopBackend(host, nil)

	ctx, cancel := context.WithCancel(context.Background())
	if err := b.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel()

	b.Stop()
	b.Stop() // should not panic or deadlock
}

func TestGitHubLoopBackend_QueueWorkNoop(t *testing.T) {
	host := &mockLoopHost{intervalMS: 100}
	b := NewGitHubLoopBackend(host, nil)
	if err := b.QueueWork(nil); err != nil {
		t.Errorf("QueueWork: %v", err)
	}
}

func TestGitHubLoopBackend_ActiveWorkNil(t *testing.T) {
	host := &mockLoopHost{intervalMS: 100}
	b := NewGitHubLoopBackend(host, nil)
	if got := b.ActiveWork(); got != nil {
		t.Errorf("ActiveWork = %v, want nil", got)
	}
}

func TestGitHubLoopBackend_OnProgress(t *testing.T) {
	host := &mockLoopHost{intervalMS: 100}
	b := NewGitHubLoopBackend(host, nil)

	var called bool
	b.OnProgress(func(_ BackendEvent) { called = true })

	if len(b.callbacks) != 1 {
		t.Errorf("callbacks len = %d, want 1", len(b.callbacks))
	}

	b.callbacks[0](BackendEvent{Type: "test"})
	if !called {
		t.Error("callback was not invoked")
	}
}
