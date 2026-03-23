package types

import (
	"fmt"
	"time"
)

type Status string

const (
	StatusQueued        Status = "queued"
	StatusPlanned       Status = "planned"
	StatusRunning       Status = "running"
	StatusBlocked       Status = "blocked"
	StatusRetryQueued   Status = "retryQueued"
	StatusNeedsApproval Status = "needsApproval"
	StatusCompleted     Status = "completed"
	StatusFailed        Status = "failed"
	StatusCanceled      Status = "canceled"
	StatusSkipped       Status = "skipped"
)

func (s Status) IsTerminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCanceled || s == StatusSkipped
}

var validTransitions = map[Status][]Status{
	StatusQueued:        {StatusPlanned, StatusRunning, StatusCanceled, StatusSkipped},
	StatusPlanned:       {StatusRunning, StatusSkipped, StatusCanceled},
	StatusRunning:       {StatusCompleted, StatusFailed, StatusRetryQueued, StatusBlocked, StatusCanceled},
	StatusRetryQueued:   {StatusRunning},
	StatusBlocked:       {StatusQueued, StatusNeedsApproval},
	StatusNeedsApproval: {StatusQueued, StatusCanceled},
}

func Transition(from, to Status) error {
	allowed, ok := validTransitions[from]
	if !ok {
		return fmt.Errorf("invalid transition: %s -> %s", from, to)
	}
	for _, s := range allowed {
		if s == to {
			return nil
		}
	}
	return fmt.Errorf("invalid transition: %s -> %s", from, to)
}

func StatusToLabel(status Status) string {
	switch status {
	case StatusQueued, StatusPlanned, StatusRetryQueued:
		return "todo"
	case StatusRunning:
		return "in-progress"
	case StatusBlocked:
		return "blocked"
	case StatusNeedsApproval:
		return "needs-approval"
	case StatusSkipped:
		return "skipped"
	case StatusCompleted:
		return "done"
	case StatusFailed:
		return "failed"
	case StatusCanceled:
		return "canceled"
	default:
		return "todo"
	}
}

func LabelToStatus(labels []string) Status {
	set := make(map[string]bool, len(labels))
	for _, l := range labels {
		set[l] = true
	}

	switch {
	case set["in-progress"]:
		return StatusRunning
	case set["blocked"]:
		return StatusBlocked
	case set["needs-approval"]:
		return StatusNeedsApproval
	case set["done"]:
		return StatusCompleted
	case set["failed"]:
		return StatusFailed
	case set["canceled"]:
		return StatusCanceled
	case set["skipped"]:
		return StatusSkipped
	default:
		return StatusQueued
	}
}

type Task struct {
	ID             string
	Identifier     string // e.g. "GH-42" or "PROJ-123"
	Title          string
	Body           string
	Labels         []string
	Status         Status
	Priority       int
	CreatedAt      time.Time
	RepoURL        string
	Metadata       map[string]string
	TerminalReason string
}

type RunOpts struct {
	WorkspacePath  string
	Prompt         string
	MaxTurns       int
	AllowedTools   []string
	MCPConfig      string
	Model          string
	StallTimeoutMS int
	PermissionMode string
	DeniedTools    []string
}

type ContinueOpts struct {
	WorkspacePath  string
	StallTimeoutMS int
	AllowedTools   []string
	PermissionMode string
}

type RunResult struct {
	SessionID string
	ExitCode  int
	Output    string
	TokensIn  int
	TokensOut int
	CostUSD   float64
	TurnsUsed int
	Duration  time.Duration
}

type Event struct {
	Type      string
	TaskID    string
	Timestamp time.Time
	Data      any
}
