package maintenance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rauriemo/anthem/internal/audit"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/types"
)

type mockAuditLogger struct {
	events []audit.AuditEvent
	mu     sync.Mutex
}

func (m *mockAuditLogger) Record(_ context.Context, event audit.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *mockAuditLogger) Query(_ context.Context, filter audit.QueryFilter) ([]audit.AuditEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []audit.AuditEvent
	for _, ev := range m.events {
		if filter.EventType != "" && ev.EventType != filter.EventType {
			continue
		}
		if filter.TaskID != "" && (ev.TaskID == nil || *ev.TaskID != filter.TaskID) {
			continue
		}
		if !filter.Since.IsZero() && ev.Timestamp.Before(filter.Since) {
			continue
		}
		result = append(result, ev)
	}
	return result, nil
}

func (m *mockAuditLogger) RecentByTask(_ context.Context, _ string, _ int) ([]audit.AuditEvent, error) {
	return nil, nil
}

func (m *mockAuditLogger) SummaryForWave(_ context.Context, _ string) (*audit.WaveSummary, error) {
	return nil, nil
}

func (m *mockAuditLogger) Close() error { return nil }

type mockEventPublisher struct {
	events []types.Event
	mu     sync.Mutex
}

func (m *mockEventPublisher) Publish(event types.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

func (m *mockEventPublisher) published() []types.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]types.Event, len(m.events))
	copy(cp, m.events)
	return cp
}

func strPtr(s string) *string   { return &s }
func f64Ptr(f float64) *float64 { return &f }

func defaultTestConfig() config.MaintenanceConfig {
	return config.MaintenanceConfig{
		ScanIntervalMS:        600000,
		FailureThreshold:      3,
		StaleThresholdHours:   24,
		CostAnomalyMultiplier: 2.0,
	}
}

func TestCheckRepeatedFailures_AboveThreshold(t *testing.T) {
	al := &mockAuditLogger{}
	for i := 0; i < 4; i++ {
		al.events = append(al.events, audit.AuditEvent{
			Timestamp: time.Now().Add(-time.Duration(i) * time.Hour),
			EventType: "task.failed",
			TaskID:    strPtr("task-1"),
		})
	}
	// task-2 has only 2 failures (below threshold)
	for i := 0; i < 2; i++ {
		al.events = append(al.events, audit.AuditEvent{
			Timestamp: time.Now().Add(-time.Duration(i) * time.Hour),
			EventType: "task.failed",
			TaskID:    strPtr("task-2"),
		})
	}

	ep := &mockEventPublisher{}
	s := NewScanner(al, ep, defaultTestConfig(), nil)

	signals := s.scan(context.Background())

	var found bool
	for _, sig := range signals {
		if sig.Kind == SignalRepeatedFailure && sig.TaskID == "task-1" {
			found = true
		}
		if sig.TaskID == "task-2" {
			t.Error("task-2 should not trigger (below threshold)")
		}
	}
	if !found {
		t.Error("expected repeated_failure signal for task-1")
	}
}

func TestCheckRepeatedFailures_BelowThreshold(t *testing.T) {
	al := &mockAuditLogger{}
	al.events = append(al.events, audit.AuditEvent{
		Timestamp: time.Now(),
		EventType: "task.failed",
		TaskID:    strPtr("task-1"),
	})

	ep := &mockEventPublisher{}
	s := NewScanner(al, ep, defaultTestConfig(), nil)

	signals := s.scan(context.Background())
	for _, sig := range signals {
		if sig.Kind == SignalRepeatedFailure {
			t.Error("should not trigger repeated_failure for single failure")
		}
	}
}

func TestCheckStaleTasks(t *testing.T) {
	al := &mockAuditLogger{}
	// Dispatched 30 hours ago, no completion
	al.events = append(al.events, audit.AuditEvent{
		Timestamp: time.Now().Add(-30 * time.Hour),
		EventType: "task.dispatched",
		TaskID:    strPtr("stale-1"),
	})
	// Dispatched 30 hours ago but completed
	al.events = append(al.events, audit.AuditEvent{
		Timestamp: time.Now().Add(-30 * time.Hour),
		EventType: "task.dispatched",
		TaskID:    strPtr("done-1"),
	})
	al.events = append(al.events, audit.AuditEvent{
		Timestamp: time.Now().Add(-1 * time.Hour),
		EventType: "task.completed",
		TaskID:    strPtr("done-1"),
	})

	ep := &mockEventPublisher{}
	s := NewScanner(al, ep, defaultTestConfig(), nil)

	signals := s.scan(context.Background())

	var foundStale bool
	for _, sig := range signals {
		if sig.Kind == SignalStaleTask && sig.TaskID == "stale-1" {
			foundStale = true
		}
		if sig.Kind == SignalStaleTask && sig.TaskID == "done-1" {
			t.Error("done-1 should not be stale")
		}
	}
	if !foundStale {
		t.Error("expected stale_task signal for stale-1")
	}
}

func TestCheckBudgetAnomalies(t *testing.T) {
	al := &mockAuditLogger{}
	// 3 tasks: costs 1.0, 1.0, 5.0 -> avg=2.33, threshold=4.67 -> task-3 triggers
	al.events = append(al.events,
		audit.AuditEvent{EventType: "task.dispatched", TaskID: strPtr("t1"), CostUSD: f64Ptr(1.0), Timestamp: time.Now()},
		audit.AuditEvent{EventType: "task.dispatched", TaskID: strPtr("t2"), CostUSD: f64Ptr(1.0), Timestamp: time.Now()},
		audit.AuditEvent{EventType: "task.dispatched", TaskID: strPtr("t3"), CostUSD: f64Ptr(5.0), Timestamp: time.Now()},
	)

	ep := &mockEventPublisher{}
	s := NewScanner(al, ep, defaultTestConfig(), nil)

	signals := s.scan(context.Background())

	var found bool
	for _, sig := range signals {
		if sig.Kind == SignalBudgetAnomaly && sig.TaskID == "t3" {
			found = true
		}
	}
	if !found {
		t.Error("expected budget_anomaly signal for t3")
	}
}

func TestCheckDrift(t *testing.T) {
	al := &mockAuditLogger{}
	// task-1: completed, then re-dispatched
	al.events = append(al.events,
		audit.AuditEvent{EventType: "task.completed", TaskID: strPtr("drift-1"), Timestamp: time.Now().Add(-2 * time.Hour)},
		audit.AuditEvent{EventType: "task.dispatched", TaskID: strPtr("drift-1"), Timestamp: time.Now().Add(-1 * time.Hour)},
	)
	// task-2: dispatched then completed (normal, no drift)
	al.events = append(al.events,
		audit.AuditEvent{EventType: "task.dispatched", TaskID: strPtr("normal-1"), Timestamp: time.Now().Add(-3 * time.Hour)},
		audit.AuditEvent{EventType: "task.completed", TaskID: strPtr("normal-1"), Timestamp: time.Now().Add(-1 * time.Hour)},
	)

	ep := &mockEventPublisher{}
	s := NewScanner(al, ep, defaultTestConfig(), nil)

	signals := s.scan(context.Background())

	var foundDrift bool
	for _, sig := range signals {
		if sig.Kind == SignalDrift && sig.TaskID == "drift-1" {
			foundDrift = true
		}
		if sig.Kind == SignalDrift && sig.TaskID == "normal-1" {
			t.Error("normal-1 should not trigger drift")
		}
	}
	if !foundDrift {
		t.Error("expected drift signal for drift-1")
	}
}

func TestAutoApproveConfig(t *testing.T) {
	al := &mockAuditLogger{}
	for i := 0; i < 5; i++ {
		al.events = append(al.events, audit.AuditEvent{
			Timestamp: time.Now(),
			EventType: "task.failed",
			TaskID:    strPtr("auto-1"),
		})
	}

	ep := &mockEventPublisher{}
	cfg := defaultTestConfig()
	cfg.AutoApprove = []string{"repeated_failure"}
	s := NewScanner(al, ep, cfg, nil)

	signals := s.scan(context.Background())

	var found bool
	for _, sig := range signals {
		if sig.Kind == SignalRepeatedFailure && sig.TaskID == "auto-1" {
			found = true
			if !sig.AutoApprove {
				t.Error("expected AutoApprove=true for repeated_failure")
			}
		}
	}
	if !found {
		t.Error("expected signal for auto-1")
	}
}

func TestScanPublishesEvents(t *testing.T) {
	al := &mockAuditLogger{}
	for i := 0; i < 4; i++ {
		al.events = append(al.events, audit.AuditEvent{
			Timestamp: time.Now(),
			EventType: "task.failed",
			TaskID:    strPtr("pub-1"),
		})
	}

	ep := &mockEventPublisher{}
	s := NewScanner(al, ep, defaultTestConfig(), nil)

	s.scan(context.Background())

	published := ep.published()
	var found bool
	for _, ev := range published {
		if ev.Type == "maintenance.suggested" && ev.TaskID == "pub-1" {
			found = true
			sig, ok := ev.Data.(Signal)
			if !ok {
				t.Error("expected Signal in event Data")
			} else if sig.Kind != SignalRepeatedFailure {
				t.Errorf("expected repeated_failure, got %s", sig.Kind)
			}
		}
	}
	if !found {
		t.Error("expected maintenance.suggested event for pub-1")
	}
}

func TestStartAndClose(t *testing.T) {
	al := &mockAuditLogger{}
	ep := &mockEventPublisher{}
	cfg := defaultTestConfig()
	cfg.ScanIntervalMS = 50

	s := NewScanner(al, ep, cfg, nil)
	ctx := context.Background()
	s.Start(ctx)
	time.Sleep(20 * time.Millisecond)
	s.Close()
}
