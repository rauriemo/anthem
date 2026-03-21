package agent

import (
	"context"

	"github.com/rauriemo/anthem/internal/types"
)

type AgentRunner interface {
	Run(ctx context.Context, opts types.RunOpts) (*types.RunResult, error)
	Continue(ctx context.Context, sessionID string, prompt string) (*types.RunResult, error)
	Kill(pid int) error
}
