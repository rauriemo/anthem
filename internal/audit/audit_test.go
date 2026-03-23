package audit

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func strPtr(s string) *string   { return &s }
func f64Ptr(f float64) *float64 { return &f }

func newTestLogger(t *testing.T) *SQLiteAuditLogger {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	logger, err := NewSQLiteAuditLogger(dbPath)
	if err != nil {
		t.Fatalf("creating audit logger: %v", err)
	}
	t.Cleanup(func() { logger.Close() })
	return logger
}

func TestRecordAndQuery(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	for i := range 3 {
		err := logger.Record(ctx, AuditEvent{
			Timestamp: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
			EventType: "dispatch",
			TaskID:    strPtr("task-1"),
		})
		if err != nil {
			t.Fatalf("recording event %d: %v", i, err)
		}
	}

	events, err := logger.Query(ctx, QueryFilter{})
	if err != nil {
		t.Fatalf("querying events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// DESC order: newest first
	if !events[0].Timestamp.After(events[2].Timestamp) {
		t.Fatal("expected DESC order by timestamp")
	}
}

func TestQueryFilter(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	events := []AuditEvent{
		{Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), EventType: "dispatch", TaskID: strPtr("task-1"), WaveID: strPtr("wave-1")},
		{Timestamp: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC), EventType: "completed", TaskID: strPtr("task-2"), WaveID: strPtr("wave-1")},
		{Timestamp: time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC), EventType: "dispatch", TaskID: strPtr("task-3"), WaveID: strPtr("wave-2")},
		{Timestamp: time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC), EventType: "failed", TaskID: strPtr("task-1"), WaveID: strPtr("wave-2")},
	}
	for i, ev := range events {
		if err := logger.Record(ctx, ev); err != nil {
			t.Fatalf("recording event %d: %v", i, err)
		}
	}

	tests := []struct {
		name   string
		filter QueryFilter
		want   int
	}{
		{"by event_type dispatch", QueryFilter{EventType: "dispatch"}, 2},
		{"by event_type completed", QueryFilter{EventType: "completed"}, 1},
		{"by task_id task-1", QueryFilter{TaskID: "task-1"}, 2},
		{"by wave_id wave-1", QueryFilter{WaveID: "wave-1"}, 2},
		{"by wave_id wave-2", QueryFilter{WaveID: "wave-2"}, 2},
		{"since filter", QueryFilter{Since: time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)}, 2},
		{"with limit", QueryFilter{Limit: 1}, 1},
		{"combined filter", QueryFilter{EventType: "dispatch", WaveID: "wave-1"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := logger.Query(ctx, tt.filter)
			if err != nil {
				t.Fatalf("query error: %v", err)
			}
			if len(results) != tt.want {
				t.Fatalf("expected %d events, got %d", tt.want, len(results))
			}
		})
	}
}

func TestRecentByTask(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	for i := range 5 {
		taskID := "task-A"
		if i >= 3 {
			taskID = "task-B"
		}
		err := logger.Record(ctx, AuditEvent{
			Timestamp: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
			EventType: "dispatch",
			TaskID:    strPtr(taskID),
		})
		if err != nil {
			t.Fatalf("recording event %d: %v", i, err)
		}
	}

	eventsA, err := logger.RecentByTask(ctx, "task-A", 10)
	if err != nil {
		t.Fatalf("RecentByTask A: %v", err)
	}
	if len(eventsA) != 3 {
		t.Fatalf("expected 3 events for task-A, got %d", len(eventsA))
	}

	eventsB, err := logger.RecentByTask(ctx, "task-B", 1)
	if err != nil {
		t.Fatalf("RecentByTask B: %v", err)
	}
	if len(eventsB) != 1 {
		t.Fatalf("expected 1 event for task-B with limit, got %d", len(eventsB))
	}
}

func TestSummaryForWave(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	events := []AuditEvent{
		{Timestamp: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), EventType: "dispatch", WaveID: strPtr("wave-1"), CostUSD: f64Ptr(0.05)},
		{Timestamp: time.Date(2026, 1, 1, 10, 1, 0, 0, time.UTC), EventType: "dispatch", WaveID: strPtr("wave-1"), CostUSD: f64Ptr(0.10)},
		{Timestamp: time.Date(2026, 1, 1, 10, 2, 0, 0, time.UTC), EventType: "completed", WaveID: strPtr("wave-1"), CostUSD: f64Ptr(0.03)},
		{Timestamp: time.Date(2026, 1, 1, 10, 3, 0, 0, time.UTC), EventType: "failed", WaveID: strPtr("wave-1"), CostUSD: f64Ptr(0.02)},
	}
	for i, ev := range events {
		if err := logger.Record(ctx, ev); err != nil {
			t.Fatalf("recording event %d: %v", i, err)
		}
	}

	summary, err := logger.SummaryForWave(ctx, "wave-1")
	if err != nil {
		t.Fatalf("SummaryForWave: %v", err)
	}

	if summary.WaveID != "wave-1" {
		t.Fatalf("expected wave_id wave-1, got %s", summary.WaveID)
	}
	if summary.TasksDispatched != 2 {
		t.Fatalf("expected 2 dispatched, got %d", summary.TasksDispatched)
	}
	if summary.TasksCompleted != 1 {
		t.Fatalf("expected 1 completed, got %d", summary.TasksCompleted)
	}
	if summary.TasksFailed != 1 {
		t.Fatalf("expected 1 failed, got %d", summary.TasksFailed)
	}

	const epsilon = 0.001
	if summary.TotalCostUSD < 0.20-epsilon || summary.TotalCostUSD > 0.20+epsilon {
		t.Fatalf("expected total cost ~0.20, got %f", summary.TotalCostUSD)
	}

	expectedStart := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	expectedEnd := time.Date(2026, 1, 1, 10, 3, 0, 0, time.UTC)
	if !summary.StartedAt.Equal(expectedStart) {
		t.Fatalf("expected start %v, got %v", expectedStart, summary.StartedAt)
	}
	if !summary.EndedAt.Equal(expectedEnd) {
		t.Fatalf("expected end %v, got %v", expectedEnd, summary.EndedAt)
	}
}

func TestConcurrentWrites(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	for g := range 10 {
		wg.Add(1)
		go func(goroutine int) {
			defer wg.Done()
			for i := range 10 {
				err := logger.Record(ctx, AuditEvent{
					Timestamp: time.Now(),
					EventType: "concurrent",
					TaskID:    strPtr("task-concurrent"),
					Metadata:  strPtr("goroutine=" + itoa(goroutine) + ",i=" + itoa(i)),
				})
				if err != nil {
					t.Errorf("goroutine %d, event %d: %v", goroutine, i, err)
				}
			}
		}(g)
	}
	wg.Wait()

	events, err := logger.Query(ctx, QueryFilter{EventType: "concurrent"})
	if err != nil {
		t.Fatalf("querying concurrent events: %v", err)
	}
	if len(events) != 100 {
		t.Fatalf("expected 100 events, got %d", len(events))
	}
}

func TestCloseFlushes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "persist.db")

	logger, err := NewSQLiteAuditLogger(dbPath)
	if err != nil {
		t.Fatalf("creating logger: %v", err)
	}

	ctx := context.Background()
	for i := range 5 {
		err := logger.Record(ctx, AuditEvent{
			Timestamp: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
			EventType: "persist-test",
		})
		if err != nil {
			t.Fatalf("recording event %d: %v", i, err)
		}
	}

	if err := logger.Close(); err != nil {
		t.Fatalf("closing logger: %v", err)
	}

	logger2, err := NewSQLiteAuditLogger(dbPath)
	if err != nil {
		t.Fatalf("reopening logger: %v", err)
	}
	defer logger2.Close()

	events, err := logger2.Query(ctx, QueryFilter{EventType: "persist-test"})
	if err != nil {
		t.Fatalf("querying after reopen: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 persisted events, got %d", len(events))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
