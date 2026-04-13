package voice

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// LatencyStage identifies a stage in the voice pipeline for latency tracking.
type LatencyStage string

const (
	StageVADOnset     LatencyStage = "vad_onset"
	StageTurnCommit   LatencyStage = "turn_commit"
	StageLLMFirstTok  LatencyStage = "llm_first_token"
	StageClauseBuffer LatencyStage = "clause_buffer"
	StageTTSFirstByte LatencyStage = "tts_first_byte"
	StageAudioPublish LatencyStage = "audio_publish"
	StageTotal        LatencyStage = "total"
	StageBargeIn      LatencyStage = "barge_in"
)

// LatencyBudget defines the hard target for a stage.
var LatencyBudget = map[LatencyStage]time.Duration{
	StageVADOnset:     50 * time.Millisecond,
	StageTurnCommit:   300 * time.Millisecond,
	StageLLMFirstTok:  500 * time.Millisecond,
	StageClauseBuffer: 150 * time.Millisecond,
	StageTTSFirstByte: 100 * time.Millisecond,
	StageAudioPublish: 30 * time.Millisecond,
	StageTotal:        700 * time.Millisecond,
	StageBargeIn:      50 * time.Millisecond,
}

// LatencySample records a single timing measurement.
type LatencySample struct {
	Stage    LatencyStage
	Duration time.Duration
	TurnID   string
	Time     time.Time
}

// LatencyMonitor tracks per-stage timing and compares against budgets.
type LatencyMonitor struct {
	mu      sync.Mutex
	samples []LatencySample
	cap     int
	logger  *slog.Logger
}

// NewLatencyMonitor creates a monitor with a sample buffer capacity.
func NewLatencyMonitor(capacity int, logger *slog.Logger) *LatencyMonitor {
	if capacity <= 0 {
		capacity = 1000
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &LatencyMonitor{
		samples: make([]LatencySample, 0, capacity),
		cap:     capacity,
		logger:  logger,
	}
}

// Record logs a latency sample and warns if it exceeds the stage budget.
func (lm *LatencyMonitor) Record(stage LatencyStage, duration time.Duration, turnID string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	sample := LatencySample{
		Stage:    stage,
		Duration: duration,
		TurnID:   turnID,
		Time:     time.Now(),
	}

	if len(lm.samples) >= lm.cap {
		lm.samples = lm.samples[1:]
	}
	lm.samples = append(lm.samples, sample)

	if budget, ok := LatencyBudget[stage]; ok && duration > budget {
		lm.logger.Warn("latency budget exceeded",
			"stage", string(stage),
			"duration_ms", duration.Milliseconds(),
			"budget_ms", budget.Milliseconds(),
			"turn_id", turnID,
		)
	}
}

// StageStats returns summary statistics for a stage.
type StageStats struct {
	Stage   LatencyStage
	Count   int
	Min     time.Duration
	Max     time.Duration
	Avg     time.Duration
	P95     time.Duration
	Budget  time.Duration
	Violate int
}

// Stats computes statistics for a given stage from recent samples.
func (lm *LatencyMonitor) Stats(stage LatencyStage) StageStats {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	var durations []time.Duration
	for _, s := range lm.samples {
		if s.Stage == stage {
			durations = append(durations, s.Duration)
		}
	}

	stats := StageStats{
		Stage:  stage,
		Count:  len(durations),
		Budget: LatencyBudget[stage],
	}

	if len(durations) == 0 {
		return stats
	}

	var total time.Duration
	stats.Min = durations[0]
	stats.Max = durations[0]
	for _, d := range durations {
		total += d
		if d < stats.Min {
			stats.Min = d
		}
		if d > stats.Max {
			stats.Max = d
		}
		if d > stats.Budget {
			stats.Violate++
		}
	}
	stats.Avg = total / time.Duration(len(durations))

	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	sortDurations(sorted)
	idx := int(float64(len(sorted)) * 0.95)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	stats.P95 = sorted[idx]

	return stats
}

// AllStats returns stats for all stages that have samples.
func (lm *LatencyMonitor) AllStats() []StageStats {
	stages := []LatencyStage{
		StageVADOnset, StageTurnCommit, StageLLMFirstTok,
		StageClauseBuffer, StageTTSFirstByte, StageAudioPublish,
		StageTotal, StageBargeIn,
	}

	var result []StageStats
	for _, s := range stages {
		stats := lm.Stats(s)
		if stats.Count > 0 {
			result = append(result, stats)
		}
	}
	return result
}

// Report generates a human-readable latency report string.
func (lm *LatencyMonitor) Report() string {
	all := lm.AllStats()
	if len(all) == 0 {
		return "No latency data collected."
	}

	var result string
	for _, s := range all {
		exceeded := ""
		if s.Violate > 0 {
			exceeded = fmt.Sprintf(" [%d violations]", s.Violate)
		}
		result += fmt.Sprintf(
			"  %s: avg=%dms p95=%dms min=%dms max=%dms (budget=%dms, n=%d)%s\n",
			s.Stage, s.Avg.Milliseconds(), s.P95.Milliseconds(),
			s.Min.Milliseconds(), s.Max.Milliseconds(),
			s.Budget.Milliseconds(), s.Count, exceeded,
		)
	}
	return result
}

func sortDurations(d []time.Duration) {
	for i := 1; i < len(d); i++ {
		key := d[i]
		j := i - 1
		for j >= 0 && d[j] > key {
			d[j+1] = d[j]
			j--
		}
		d[j+1] = key
	}
}
