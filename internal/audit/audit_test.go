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
		{Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), EventType: "task.dispatched", TaskID: strPtr("task-1"), WaveID: strPtr("wave-1")},
		{Timestamp: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC), EventType: "task.completed", TaskID: strPtr("task-2"), WaveID: strPtr("wave-1")},
		{Timestamp: time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC), EventType: "task.dispatched", TaskID: strPtr("task-3"), WaveID: strPtr("wave-2")},
		{Timestamp: time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC), EventType: "task.failed", TaskID: strPtr("task-1"), WaveID: strPtr("wave-2")},
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
		{"by event_type task.dispatched", QueryFilter{EventType: "task.dispatched"}, 2},
		{"by event_type task.completed", QueryFilter{EventType: "task.completed"}, 1},
		{"by task_id task-1", QueryFilter{TaskID: "task-1"}, 2},
		{"by wave_id wave-1", QueryFilter{WaveID: "wave-1"}, 2},
		{"by wave_id wave-2", QueryFilter{WaveID: "wave-2"}, 2},
		{"since filter", QueryFilter{Since: time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)}, 2},
		{"with limit", QueryFilter{Limit: 1}, 1},
		{"combined filter", QueryFilter{EventType: "task.dispatched", WaveID: "wave-1"}, 1},
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
		{Timestamp: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), EventType: "task.dispatched", WaveID: strPtr("wave-1"), CostUSD: f64Ptr(0.05)},
		{Timestamp: time.Date(2026, 1, 1, 10, 1, 0, 0, time.UTC), EventType: "task.dispatched", WaveID: strPtr("wave-1"), CostUSD: f64Ptr(0.10)},
		{Timestamp: time.Date(2026, 1, 1, 10, 2, 0, 0, time.UTC), EventType: "task.completed", WaveID: strPtr("wave-1"), CostUSD: f64Ptr(0.03)},
		{Timestamp: time.Date(2026, 1, 1, 10, 3, 0, 0, time.UTC), EventType: "task.failed", WaveID: strPtr("wave-1"), CostUSD: f64Ptr(0.02)},
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

func TestRecordTraceAndQuery(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	trace := TraceRecord{
		Timestamp:       time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		TraceType:       "executor",
		TaskID:          strPtr("task-1"),
		SessionID:       strPtr("sess-1"),
		PromptPreview:   strPtr("Write tests for..."),
		ResponsePreview: strPtr("Here are the tests..."),
		TokensIn:        500,
		TokensOut:       200,
		CostUSD:         0.05,
		DurationMS:      3000,
	}
	if err := logger.RecordTrace(ctx, trace); err != nil {
		t.Fatalf("RecordTrace: %v", err)
	}

	traces, err := logger.TracesForTask(ctx, "task-1", 10)
	if err != nil {
		t.Fatalf("TracesForTask: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}

	tr := traces[0]
	if tr.TraceType != "executor" {
		t.Errorf("TraceType = %q, want executor", tr.TraceType)
	}
	if tr.TokensIn != 500 {
		t.Errorf("TokensIn = %d, want 500", tr.TokensIn)
	}
	if tr.CostUSD != 0.05 {
		t.Errorf("CostUSD = %f, want 0.05", tr.CostUSD)
	}
}

func TestRecentTraces(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	for i := range 5 {
		err := logger.RecordTrace(ctx, TraceRecord{
			Timestamp: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
			TraceType: "lean",
			TokensIn:  100,
		})
		if err != nil {
			t.Fatalf("RecordTrace %d: %v", i, err)
		}
	}

	traces, err := logger.RecentTraces(ctx, 3)
	if err != nil {
		t.Fatalf("RecentTraces: %v", err)
	}
	if len(traces) != 3 {
		t.Fatalf("expected 3 traces, got %d", len(traces))
	}
	// DESC order: most recent first
	if !traces[0].Timestamp.After(traces[2].Timestamp) {
		t.Error("expected DESC order")
	}
}

func TestTraceReviewFields(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	passed := true
	trace := TraceRecord{
		Timestamp:      time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		TraceType:      "reviewer",
		TaskID:         strPtr("task-1"),
		ReviewPassed:   &passed,
		ReviewFeedback: strPtr("looks good"),
	}
	if err := logger.RecordTrace(ctx, trace); err != nil {
		t.Fatalf("RecordTrace: %v", err)
	}

	traces, err := logger.TracesForTask(ctx, "task-1", 1)
	if err != nil {
		t.Fatalf("TracesForTask: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	if traces[0].ReviewPassed == nil || !*traces[0].ReviewPassed {
		t.Error("expected ReviewPassed = true")
	}
	if traces[0].ReviewFeedback == nil || *traces[0].ReviewFeedback != "looks good" {
		t.Errorf("ReviewFeedback = %v, want 'looks good'", traces[0].ReviewFeedback)
	}
}

func TestTracesForWave(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	traces := []TraceRecord{
		{Timestamp: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), TraceType: "executor", WaveID: strPtr("wave-1"), TaskID: strPtr("t1"), TokensIn: 100},
		{Timestamp: time.Date(2026, 1, 1, 10, 1, 0, 0, time.UTC), TraceType: "executor", WaveID: strPtr("wave-1"), TaskID: strPtr("t2"), TokensIn: 200},
		{Timestamp: time.Date(2026, 1, 1, 10, 2, 0, 0, time.UTC), TraceType: "reviewer", WaveID: strPtr("wave-1"), TaskID: strPtr("t1"), TokensIn: 50},
		{Timestamp: time.Date(2026, 1, 1, 10, 3, 0, 0, time.UTC), TraceType: "executor", WaveID: strPtr("wave-2"), TaskID: strPtr("t3"), TokensIn: 300},
	}
	for i, tr := range traces {
		if err := logger.RecordTrace(ctx, tr); err != nil {
			t.Fatalf("RecordTrace %d: %v", i, err)
		}
	}

	got, err := logger.TracesForWave(ctx, "wave-1", 10)
	if err != nil {
		t.Fatalf("TracesForWave: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 traces for wave-1, got %d", len(got))
	}
	if got[0].TraceType != "reviewer" {
		t.Errorf("expected most recent first (reviewer), got %s", got[0].TraceType)
	}

	limited, err := logger.TracesForWave(ctx, "wave-1", 2)
	if err != nil {
		t.Fatalf("TracesForWave with limit: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("expected 2 traces with limit, got %d", len(limited))
	}

	wave2, err := logger.TracesForWave(ctx, "wave-2", 10)
	if err != nil {
		t.Fatalf("TracesForWave wave-2: %v", err)
	}
	if len(wave2) != 1 {
		t.Fatalf("expected 1 trace for wave-2, got %d", len(wave2))
	}
}

func TestGetTraceStats(t *testing.T) {
	logger := newTestLogger(t)
	ctx := context.Background()

	traces := []TraceRecord{
		{Timestamp: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), TraceType: "executor", CostUSD: 0.10, TokensIn: 500, TokensOut: 200, DurationMS: 3000},
		{Timestamp: time.Date(2026, 1, 1, 10, 1, 0, 0, time.UTC), TraceType: "executor", CostUSD: 0.20, TokensIn: 600, TokensOut: 300, DurationMS: 5000},
		{Timestamp: time.Date(2026, 1, 1, 10, 2, 0, 0, time.UTC), TraceType: "reviewer", CostUSD: 0.03, TokensIn: 100, TokensOut: 50, DurationMS: 1000},
		{Timestamp: time.Date(2026, 1, 1, 10, 3, 0, 0, time.UTC), TraceType: "lean", CostUSD: 0.01, TokensIn: 50, TokensOut: 20, DurationMS: 500},
	}
	for i, tr := range traces {
		if err := logger.RecordTrace(ctx, tr); err != nil {
			t.Fatalf("RecordTrace %d: %v", i, err)
		}
	}

	stats, err := logger.GetTraceStats(ctx)
	if err != nil {
		t.Fatalf("GetTraceStats: %v", err)
	}
	if len(stats) != 3 {
		t.Fatalf("expected 3 trace types, got %d", len(stats))
	}

	byType := make(map[string]TraceStats)
	for _, s := range stats {
		byType[s.TraceType] = s
	}

	exec := byType["executor"]
	if exec.Count != 2 {
		t.Errorf("executor count = %d, want 2", exec.Count)
	}
	const epsilon = 0.001
	if exec.TotalCost < 0.30-epsilon || exec.TotalCost > 0.30+epsilon {
		t.Errorf("executor total cost = %f, want 0.30", exec.TotalCost)
	}
	if exec.TotalIn != 1100 {
		t.Errorf("executor TotalIn = %d, want 1100", exec.TotalIn)
	}
	if exec.TotalOut != 500 {
		t.Errorf("executor TotalOut = %d, want 500", exec.TotalOut)
	}
	if exec.AvgDurMS < 3999 || exec.AvgDurMS > 4001 {
		t.Errorf("executor AvgDurMS = %f, want 4000", exec.AvgDurMS)
	}

	rev := byType["reviewer"]
	if rev.Count != 1 {
		t.Errorf("reviewer count = %d, want 1", rev.Count)
	}

	lean := byType["lean"]
	if lean.Count != 1 {
		t.Errorf("lean count = %d, want 1", lean.Count)
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
