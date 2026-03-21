package workspace

import (
	"context"

	"github.com/rauriemo/anthem/internal/types"
)

type WorkspaceManager interface {
	Prepare(ctx context.Context, task types.Task) (workspacePath string, err error)
	RunHook(ctx context.Context, hookName string, workspacePath string) error
	Cleanup(ctx context.Context, taskID string) error
}
