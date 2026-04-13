package voice

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLatencyMonitor_RecordAndStats(t *testing.T) {
	t.Parallel()
	lm := NewLatencyMonitor(100, slog.Default())

	lm.Record(StageTurnCommit, 250*time.Millisecond, "turn-1")
	lm.Record(StageTurnCommit, 350*time.Millisecond, "turn-2")
	lm.Record(StageTurnCommit, 300*time.Millisecond, "turn-3")

	stats := lm.Stats(StageTurnCommit)
	if stats.Count != 3 {
		t.Errorf("count = %d, want 3", stats.Count)
	}
	if stats.Min != 250*time.Millisecond {
		t.Errorf("min = %v, want 250ms", stats.Min)
	}
	if stats.Max != 350*time.Millisecond {
		t.Errorf("max = %v, want 350ms", stats.Max)
	}
	if stats.Budget != 300*time.Millisecond {
		t.Errorf("budget = %v, want 300ms", stats.Budget)
	}
}

func TestLatencyMonitor_BudgetViolation(t *testing.T) {
	t.Parallel()
	lm := NewLatencyMonitor(100, slog.Default())

	lm.Record(StageVADOnset, 100*time.Millisecond, "turn-1") // 50ms budget -> violation

	stats := lm.Stats(StageVADOnset)
	if stats.Violate != 1 {
		t.Errorf("violations = %d, want 1", stats.Violate)
	}
}

func TestLatencyMonitor_NoViolation(t *testing.T) {
	t.Parallel()
	lm := NewLatencyMonitor(100, slog.Default())

	lm.Record(StageVADOnset, 30*time.Millisecond, "turn-1")

	stats := lm.Stats(StageVADOnset)
	if stats.Violate != 0 {
		t.Errorf("violations = %d, want 0", stats.Violate)
	}
}

func TestLatencyMonitor_Eviction(t *testing.T) {
	t.Parallel()
	lm := NewLatencyMonitor(3, slog.Default())

	lm.Record(StageTurnCommit, 100*time.Millisecond, "t1")
	lm.Record(StageTurnCommit, 200*time.Millisecond, "t2")
	lm.Record(StageTurnCommit, 300*time.Millisecond, "t3")
	lm.Record(StageTurnCommit, 400*time.Millisecond, "t4")

	stats := lm.Stats(StageTurnCommit)
	if stats.Count != 3 {
		t.Errorf("count = %d, want 3 (oldest evicted)", stats.Count)
	}
	if stats.Min != 200*time.Millisecond {
		t.Errorf("min = %v, want 200ms (oldest evicted)", stats.Min)
	}
}

func TestLatencyMonitor_EmptyStats(t *testing.T) {
	t.Parallel()
	lm := NewLatencyMonitor(100, slog.Default())

	stats := lm.Stats(StageTurnCommit)
	if stats.Count != 0 {
		t.Errorf("count = %d, want 0", stats.Count)
	}
}

func TestLatencyMonitor_AllStats(t *testing.T) {
	t.Parallel()
	lm := NewLatencyMonitor(100, slog.Default())

	lm.Record(StageVADOnset, 40*time.Millisecond, "t1")
	lm.Record(StageTTSFirstByte, 80*time.Millisecond, "t1")

	all := lm.AllStats()
	if len(all) != 2 {
		t.Errorf("got %d stats, want 2", len(all))
	}
}

func TestLatencyMonitor_Report(t *testing.T) {
	t.Parallel()
	lm := NewLatencyMonitor(100, slog.Default())

	lm.Record(StageTotal, 700*time.Millisecond, "t1")

	report := lm.Report()
	if !strings.Contains(report, "total") {
		t.Errorf("report should contain stage name, got: %s", report)
	}
}

func TestLatencyMonitor_ReportEmpty(t *testing.T) {
	t.Parallel()
	lm := NewLatencyMonitor(100, slog.Default())

	report := lm.Report()
	if !strings.Contains(report, "No latency data") {
		t.Errorf("empty report should say no data, got: %s", report)
	}
}

func TestLatencyBudgets_Defined(t *testing.T) {
	t.Parallel()
	stages := []LatencyStage{
		StageVADOnset, StageTurnCommit, StageLLMFirstTok,
		StageClauseBuffer, StageTTSFirstByte, StageAudioPublish,
		StageTotal, StageBargeIn,
	}
	for _, s := range stages {
		if _, ok := LatencyBudget[s]; !ok {
			t.Errorf("missing budget for stage %s", s)
		}
	}
}
