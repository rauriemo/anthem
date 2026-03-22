package types

import "time"

type Status string

const (
	StatusActive    Status = "active"
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

func (s Status) IsTerminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCanceled
}

type Task struct {
	ID         string
	Identifier string // e.g. "GH-42" or "PROJ-123"
	Title      string
	Body       string
	Labels     []string
	Status     Status
	Priority   int
	CreatedAt  time.Time
	RepoURL    string
	Metadata   map[string]string
}

type RunOpts struct {
	WorkspacePath  string
	Prompt         string
	MaxTurns       int
	AllowedTools   []string
	MCPConfig      string
	Model          string
	StallTimeoutMS int
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
