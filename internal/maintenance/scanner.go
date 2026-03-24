package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/rauriemo/anthem/internal/audit"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/types"
)

type SignalKind string

const (
	SignalRepeatedFailure SignalKind = "repeated_failure"
	SignalStaleTask       SignalKind = "stale_task"
	SignalBudgetAnomaly   SignalKind = "budget_anomaly"
	SignalDrift           SignalKind = "drift"
)

type Signal struct {
	Kind        SignalKind
	TaskID      string
	Description string
	AutoApprove bool
}

func (s Signal) String() string { return s.Description }

type EventPublisher interface {
	Publish(event types.Event)
}

type Scanner struct {
	audit  audit.AuditLogger
	events EventPublisher
	config config.MaintenanceConfig
	logger *slog.Logger
	cancel context.CancelFunc
}

func NewScanner(auditLog audit.AuditLogger, events EventPublisher, cfg config.MaintenanceConfig, logger *slog.Logger) *Scanner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scanner{
		audit:  auditLog,
		events: events,
		config: cfg,
		logger: logger,
	}
}

func (s *Scanner) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	interval := time.Duration(s.config.ScanIntervalMS) * time.Millisecond
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.scan(ctx)
			}
		}
	}()
}

func (s *Scanner) Close() {
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *Scanner) scan(ctx context.Context) []Signal {
	var signals []Signal
	signals = append(signals, s.checkRepeatedFailures(ctx)...)
	signals = append(signals, s.checkStaleTasks(ctx)...)
	signals = append(signals, s.checkBudgetAnomalies(ctx)...)
	signals = append(signals, s.checkDrift(ctx)...)

	for i := range signals {
		signals[i].AutoApprove = s.isAutoApproved(signals[i].Kind)
		s.events.Publish(types.Event{
			Type:      "maintenance.suggested",
			TaskID:    signals[i].TaskID,
			Timestamp: time.Now(),
			Data:      signals[i],
		})
	}

	if len(signals) > 0 {
		s.logger.Info("maintenance scan found signals", "count", len(signals))
	}

	return signals
}

func (s *Scanner) checkRepeatedFailures(ctx context.Context) []Signal {
	since := time.Now().Add(-24 * time.Hour)
	events, err := s.audit.Query(ctx, audit.QueryFilter{
		EventType: "task.failed",
		Since:     since,
	})
	if err != nil {
		s.logger.Warn("maintenance: failed to query failures", "error", err)
		return nil
	}

	counts := make(map[string]int)
	for _, ev := range events {
		if ev.TaskID != nil {
			counts[*ev.TaskID]++
		}
	}

	var signals []Signal
	for taskID, count := range counts {
		if count >= s.config.FailureThreshold {
			signals = append(signals, Signal{
				Kind:        SignalRepeatedFailure,
				TaskID:      taskID,
				Description: fmt.Sprintf("Task %s has failed %d times in the last 24 hours.", taskID, count),
			})
		}
	}
	return signals
}

func (s *Scanner) checkStaleTasks(ctx context.Context) []Signal {
	// Query all dispatched events (no Since filter — we need old ones that might be stale)
	dispatched, err := s.audit.Query(ctx, audit.QueryFilter{
		EventType: "task.dispatched",
	})
	if err != nil {
		s.logger.Warn("maintenance: failed to query dispatched tasks", "error", err)
		return nil
	}

	dispatchedIDs := make(map[string]time.Time)
	for _, ev := range dispatched {
		if ev.TaskID != nil {
			if _, exists := dispatchedIDs[*ev.TaskID]; !exists {
				dispatchedIDs[*ev.TaskID] = ev.Timestamp
			}
		}
	}

	completed, err := s.audit.Query(ctx, audit.QueryFilter{
		EventType: "task.completed",
	})
	if err != nil {
		s.logger.Warn("maintenance: failed to query completed tasks", "error", err)
		return nil
	}
	for _, ev := range completed {
		if ev.TaskID != nil {
			delete(dispatchedIDs, *ev.TaskID)
		}
	}

	failed, err := s.audit.Query(ctx, audit.QueryFilter{
		EventType: "task.failed",
	})
	if err != nil {
		s.logger.Warn("maintenance: failed to query failed tasks", "error", err)
		return nil
	}
	for _, ev := range failed {
		if ev.TaskID != nil {
			delete(dispatchedIDs, *ev.TaskID)
		}
	}

	var signals []Signal
	for taskID, dispatchTime := range dispatchedIDs {
		hours := int(time.Since(dispatchTime).Hours())
		if hours >= s.config.StaleThresholdHours {
			signals = append(signals, Signal{
				Kind:        SignalStaleTask,
				TaskID:      taskID,
				Description: fmt.Sprintf("Task %s has been dispatched for over %d hours.", taskID, hours),
			})
		}
	}
	return signals
}

func (s *Scanner) checkBudgetAnomalies(ctx context.Context) []Signal {
	events, err := s.audit.Query(ctx, audit.QueryFilter{})
	if err != nil {
		s.logger.Warn("maintenance: failed to query for budget anomalies", "error", err)
		return nil
	}

	taskCosts := make(map[string]float64)
	for _, ev := range events {
		if ev.CostUSD != nil && ev.TaskID != nil && *ev.CostUSD > 0 {
			taskCosts[*ev.TaskID] += *ev.CostUSD
		}
	}

	if len(taskCosts) == 0 {
		return nil
	}

	var total float64
	for _, cost := range taskCosts {
		total += cost
	}
	avg := total / float64(len(taskCosts))
	threshold := s.config.CostAnomalyMultiplier * avg

	var signals []Signal
	for taskID, cost := range taskCosts {
		if cost > threshold {
			signals = append(signals, Signal{
				Kind:        SignalBudgetAnomaly,
				TaskID:      taskID,
				Description: fmt.Sprintf("Task %s cost $%.2f exceeds %.1fx the average ($%.2f).", taskID, cost, s.config.CostAnomalyMultiplier, avg),
			})
		}
	}
	return signals
}

func (s *Scanner) checkDrift(ctx context.Context) []Signal {
	completed, err := s.audit.Query(ctx, audit.QueryFilter{EventType: "task.completed"})
	if err != nil {
		s.logger.Warn("maintenance: failed to query completed tasks for drift", "error", err)
		return nil
	}

	completedTimes := make(map[string]time.Time)
	for _, ev := range completed {
		if ev.TaskID != nil {
			if existing, ok := completedTimes[*ev.TaskID]; !ok || ev.Timestamp.After(existing) {
				completedTimes[*ev.TaskID] = ev.Timestamp
			}
		}
	}

	dispatched, err := s.audit.Query(ctx, audit.QueryFilter{EventType: "task.dispatched"})
	if err != nil {
		s.logger.Warn("maintenance: failed to query dispatched tasks for drift", "error", err)
		return nil
	}

	var signals []Signal
	seen := make(map[string]bool)
	for _, ev := range dispatched {
		if ev.TaskID == nil {
			continue
		}
		taskID := *ev.TaskID
		if seen[taskID] {
			continue
		}
		completedAt, wasCompleted := completedTimes[taskID]
		if wasCompleted && ev.Timestamp.After(completedAt) {
			signals = append(signals, Signal{
				Kind:        SignalDrift,
				TaskID:      taskID,
				Description: fmt.Sprintf("Task %s was completed but has been re-dispatched.", taskID),
			})
			seen[taskID] = true
		}
	}
	return signals
}

func (s *Scanner) isAutoApproved(kind SignalKind) bool {
	for _, approved := range s.config.AutoApprove {
		if approved == string(kind) {
			return true
		}
	}
	return false
}
