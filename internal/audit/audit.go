package audit

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type AuditEvent struct {
	Timestamp    time.Time
	EventType    string
	TaskID       *string
	SessionID    *string
	WaveID       *string
	ActionName   *string
	ActionInput  *string
	ActionOutput *string
	RiskLevel    *string
	AutoApproved *bool
	CostUSD      *float64
	Error        *string
	Metadata     *string
}

type QueryFilter struct {
	EventType string
	TaskID    string
	WaveID    string
	Since     time.Time
	Limit     int
}

type WaveSummary struct {
	WaveID          string
	TasksDispatched int
	TasksCompleted  int
	TasksFailed     int
	TotalCostUSD    float64
	StartedAt       time.Time
	EndedAt         time.Time
}

type AuditLogger interface {
	Record(ctx context.Context, event AuditEvent) error
	Query(ctx context.Context, filter QueryFilter) ([]AuditEvent, error)
	RecentByTask(ctx context.Context, taskID string, limit int) ([]AuditEvent, error)
	SummaryForWave(ctx context.Context, waveID string) (*WaveSummary, error)
	Close() error
}

type SQLiteAuditLogger struct {
	db *sql.DB
	mu sync.Mutex
}

func NewSQLiteAuditLogger(dbPath string) (*SQLiteAuditLogger, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating audit db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening audit db: %w", err)
	}

	// busy_timeout lets SQLite retry internally on SQLITE_BUSY instead of failing immediately.
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting busy timeout: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling WAL mode: %w", err)
	}

	if _, err := db.Exec(CreateTableSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating audit schema: %w", err)
	}

	return &SQLiteAuditLogger{db: db}, nil
}

func (l *SQLiteAuditLogger) Record(ctx context.Context, event AuditEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var autoApproved *int
	if event.AutoApproved != nil {
		v := 0
		if *event.AutoApproved {
			v = 1
		}
		autoApproved = &v
	}

	_, err := l.db.ExecContext(ctx,
		`INSERT INTO events (timestamp, event_type, task_id, session_id, wave_id,
			action_name, action_input, action_output, risk_level, auto_approved,
			cost_usd, error, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.Timestamp.Format(time.RFC3339Nano),
		event.EventType,
		event.TaskID,
		event.SessionID,
		event.WaveID,
		event.ActionName,
		event.ActionInput,
		event.ActionOutput,
		event.RiskLevel,
		autoApproved,
		event.CostUSD,
		event.Error,
		event.Metadata,
	)
	if err != nil {
		return fmt.Errorf("recording audit event: %w", err)
	}
	return nil
}

func (l *SQLiteAuditLogger) Query(ctx context.Context, filter QueryFilter) ([]AuditEvent, error) {
	var clauses []string
	var args []any

	if filter.EventType != "" {
		clauses = append(clauses, "event_type = ?")
		args = append(args, filter.EventType)
	}
	if filter.TaskID != "" {
		clauses = append(clauses, "task_id = ?")
		args = append(args, filter.TaskID)
	}
	if filter.WaveID != "" {
		clauses = append(clauses, "wave_id = ?")
		args = append(args, filter.WaveID)
	}
	if !filter.Since.IsZero() {
		clauses = append(clauses, "timestamp >= ?")
		args = append(args, filter.Since.Format(time.RFC3339Nano))
	}

	query := "SELECT timestamp, event_type, task_id, session_id, wave_id, action_name, action_input, action_output, risk_level, auto_approved, cost_usd, error, metadata FROM events"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY timestamp DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}

	return l.queryRows(ctx, query, args...)
}

func (l *SQLiteAuditLogger) RecentByTask(ctx context.Context, taskID string, limit int) ([]AuditEvent, error) {
	return l.Query(ctx, QueryFilter{TaskID: taskID, Limit: limit})
}

func (l *SQLiteAuditLogger) SummaryForWave(ctx context.Context, waveID string) (*WaveSummary, error) {
	row := l.db.QueryRowContext(ctx,
		`SELECT
			COALESCE(SUM(CASE WHEN event_type = 'dispatch' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN event_type = 'completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN event_type = 'failed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(MIN(timestamp), ''),
			COALESCE(MAX(timestamp), '')
		FROM events WHERE wave_id = ?`, waveID)

	var dispatched, completed, failed int
	var totalCost float64
	var startStr, endStr string

	if err := row.Scan(&dispatched, &completed, &failed, &totalCost, &startStr, &endStr); err != nil {
		return nil, fmt.Errorf("querying wave summary: %w", err)
	}

	summary := &WaveSummary{
		WaveID:          waveID,
		TasksDispatched: dispatched,
		TasksCompleted:  completed,
		TasksFailed:     failed,
		TotalCostUSD:    totalCost,
	}

	if startStr != "" {
		if t, err := time.Parse(time.RFC3339Nano, startStr); err == nil {
			summary.StartedAt = t
		}
	}
	if endStr != "" {
		if t, err := time.Parse(time.RFC3339Nano, endStr); err == nil {
			summary.EndedAt = t
		}
	}

	return summary, nil
}

func (l *SQLiteAuditLogger) Close() error {
	return l.db.Close()
}

func (l *SQLiteAuditLogger) queryRows(ctx context.Context, query string, args ...any) ([]AuditEvent, error) {
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying audit events: %w", err)
	}
	defer rows.Close()

	var events []AuditEvent
	for rows.Next() {
		var (
			tsStr        string
			eventType    string
			taskID       *string
			sessionID    *string
			waveID       *string
			actionName   *string
			actionInput  *string
			actionOutput *string
			riskLevel    *string
			autoApproved *int
			costUSD      *float64
			errStr       *string
			metadata     *string
		)
		if err := rows.Scan(&tsStr, &eventType, &taskID, &sessionID, &waveID,
			&actionName, &actionInput, &actionOutput, &riskLevel, &autoApproved,
			&costUSD, &errStr, &metadata); err != nil {
			return nil, fmt.Errorf("scanning audit event: %w", err)
		}

		ts, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			return nil, fmt.Errorf("parsing audit event timestamp: %w", err)
		}

		ev := AuditEvent{
			Timestamp:    ts,
			EventType:    eventType,
			TaskID:       taskID,
			SessionID:    sessionID,
			WaveID:       waveID,
			ActionName:   actionName,
			ActionInput:  actionInput,
			ActionOutput: actionOutput,
			RiskLevel:    riskLevel,
			CostUSD:      costUSD,
			Error:        errStr,
			Metadata:     metadata,
		}

		if autoApproved != nil {
			b := *autoApproved != 0
			ev.AutoApproved = &b
		}

		events = append(events, ev)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating audit events: %w", err)
	}

	return events, nil
}
