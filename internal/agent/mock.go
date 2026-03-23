package agent

import (
	"context"
	"time"

	"github.com/rauriemo/anthem/internal/types"
)

type MockRunner struct {
	RunFunc      func(ctx context.Context, opts types.RunOpts) (*types.RunResult, error)
	ContinueFunc func(ctx context.Context, sessionID string, prompt string, opts types.ContinueOpts) (*types.RunResult, error)
	KillFunc     func(pid int) error
}

func NewMockRunner() *MockRunner {
	return &MockRunner{
		RunFunc: func(_ context.Context, _ types.RunOpts) (*types.RunResult, error) {
			return &types.RunResult{
				SessionID: "mock-session",
				ExitCode:  0,
				Output:    "mock output",
				Duration:  time.Second,
			}, nil
		},
		ContinueFunc: func(_ context.Context, _ string, _ string, _ types.ContinueOpts) (*types.RunResult, error) {
			return &types.RunResult{
				SessionID: "mock-session",
				ExitCode:  0,
				Output:    "mock continue output",
				Duration:  time.Second,
			}, nil
		},
		KillFunc: func(_ int) error { return nil },
	}
}

func (m *MockRunner) Run(ctx context.Context, opts types.RunOpts) (*types.RunResult, error) {
	return m.RunFunc(ctx, opts)
}

func (m *MockRunner) Continue(ctx context.Context, sessionID string, prompt string, opts types.ContinueOpts) (*types.RunResult, error) {
	return m.ContinueFunc(ctx, sessionID, prompt, opts)
}

func (m *MockRunner) Kill(pid int) error {
	return m.KillFunc(pid)
}
