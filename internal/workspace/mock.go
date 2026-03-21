package workspace

import (
	"context"

	"github.com/rauriemo/anthem/internal/types"
)

type MockWorkspaceManager struct {
	PrepareFunc func(ctx context.Context, task types.Task) (string, error)
	RunHookFunc func(ctx context.Context, hookName string, workspacePath string) error
	CleanupFunc func(ctx context.Context, taskID string) error
}

func NewMockWorkspaceManager() *MockWorkspaceManager {
	return &MockWorkspaceManager{
		PrepareFunc: func(_ context.Context, task types.Task) (string, error) {
			return "/tmp/anthem-workspace/" + task.Identifier, nil
		},
		RunHookFunc: func(_ context.Context, _ string, _ string) error { return nil },
		CleanupFunc: func(_ context.Context, _ string) error { return nil },
	}
}

func (m *MockWorkspaceManager) Prepare(ctx context.Context, task types.Task) (string, error) {
	return m.PrepareFunc(ctx, task)
}

func (m *MockWorkspaceManager) RunHook(ctx context.Context, hookName string, workspacePath string) error {
	return m.RunHookFunc(ctx, hookName, workspacePath)
}

func (m *MockWorkspaceManager) Cleanup(ctx context.Context, taskID string) error {
	return m.CleanupFunc(ctx, taskID)
}
