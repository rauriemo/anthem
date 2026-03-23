package workspace

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/types"
)

type WorkspaceManager interface {
	Prepare(ctx context.Context, task types.Task) (workspacePath string, err error)
	RunHook(ctx context.Context, hookName string, workspacePath string) error
	Cleanup(ctx context.Context, taskID string) error
}

// Manager is the production workspace manager that creates per-task directories
// and runs lifecycle hooks.
type Manager struct {
	root   string
	hooks  config.HooksConfig
	logger *slog.Logger
}

func NewManager(root string, hooks config.HooksConfig, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		root:   root,
		hooks:  hooks,
		logger: logger,
	}
}

func (m *Manager) Prepare(ctx context.Context, task types.Task) (string, error) {
	wsPath := filepath.Join(m.root, task.ID)

	absPath, err := filepath.Abs(wsPath)
	if err != nil {
		return "", fmt.Errorf("resolving workspace path: %w", err)
	}

	if err := ValidatePath(m.root, absPath); err != nil {
		return "", fmt.Errorf("workspace path validation failed: %w", err)
	}

	if err := os.MkdirAll(absPath, 0755); err != nil {
		return "", fmt.Errorf("creating workspace directory: %w", err)
	}

	if m.hooks.AfterCreate != "" {
		if err := m.RunHook(ctx, "after_create", absPath); err != nil {
			return "", fmt.Errorf("after_create hook failed: %w", err)
		}
	}

	return absPath, nil
}

func (m *Manager) RunHook(ctx context.Context, hookName string, workspacePath string) error {
	hookCmd := m.hookCommand(hookName)
	if hookCmd == "" {
		return nil
	}

	switch hookName {
	case "before_run":
		return m.runWithRetry(ctx, hookCmd, workspacePath, 3)
	case "after_complete":
		if err := m.execHook(ctx, hookCmd, workspacePath); err != nil {
			m.logger.Warn("after_complete hook failed (non-fatal)", "error", err)
			return nil
		}
		return nil
	default:
		return m.execHook(ctx, hookCmd, workspacePath)
	}
}

func (m *Manager) Cleanup(_ context.Context, taskID string) error {
	wsPath := filepath.Join(m.root, taskID)

	absPath, err := filepath.Abs(wsPath)
	if err != nil {
		return fmt.Errorf("resolving workspace path: %w", err)
	}

	if err := ValidatePath(m.root, absPath); err != nil {
		return fmt.Errorf("cleanup path validation failed: %w", err)
	}

	if err := os.RemoveAll(absPath); err != nil {
		return fmt.Errorf("removing workspace directory: %w", err)
	}

	return nil
}

// CleanupTerminal removes workspace directories for tasks in terminal states.
func (m *Manager) CleanupTerminal(terminalTaskIDs []string) {
	if len(terminalTaskIDs) == 0 {
		return
	}

	terminal := make(map[string]bool, len(terminalTaskIDs))
	for _, id := range terminalTaskIDs {
		terminal[id] = true
	}

	entries, err := os.ReadDir(m.root)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		m.logger.Warn("failed to scan workspace root for cleanup", "root", m.root, "error", err)
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if terminal[entry.Name()] {
			wsPath := filepath.Join(m.root, entry.Name())
			if err := os.RemoveAll(wsPath); err != nil {
				m.logger.Warn("failed to clean up terminal workspace", "path", wsPath, "error", err)
			} else {
				m.logger.Info("cleaned up terminal workspace", "task_id", entry.Name())
			}
		}
	}
}

func (m *Manager) hookCommand(hookName string) string {
	switch hookName {
	case "after_create":
		return m.hooks.AfterCreate
	case "before_run":
		return m.hooks.BeforeRun
	case "after_complete":
		return m.hooks.AfterComplete
	default:
		return ""
	}
}

func (m *Manager) runWithRetry(ctx context.Context, hookCmd string, workspacePath string, maxAttempts int) error {
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		if err := m.execHook(ctx, hookCmd, workspacePath); err != nil {
			lastErr = err
			m.logger.Warn("hook failed, retrying",
				"hook", "before_run",
				"attempt", i+1,
				"max_attempts", maxAttempts,
				"error", err,
			)
			if i < maxAttempts-1 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(1 * time.Second):
				}
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("hook failed after %d attempts: %w", maxAttempts, lastErr)
}

func (m *Manager) execHook(ctx context.Context, hookCmd string, workspacePath string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", hookCmd)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", hookCmd)
	}
	cmd.Dir = workspacePath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hook command %q failed: %w (stderr: %s)", hookCmd, err, stderr.String())
	}

	return nil
}
