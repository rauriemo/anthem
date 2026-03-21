//go:build windows

package claude

import "os/exec"

// WindowsProcessManager uses Job Objects to manage process trees.
type WindowsProcessManager struct{}

func NewPlatformProcessManager() *WindowsProcessManager {
	return &WindowsProcessManager{}
}

func (w *WindowsProcessManager) Start(cmd *exec.Cmd) error {
	// Phase 1: create Job Object, assign process, start
	return cmd.Start()
}

func (w *WindowsProcessManager) Terminate(cmd *exec.Cmd) error {
	// Phase 1: TerminateJobObject for graceful shutdown
	if cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return nil
}

func (w *WindowsProcessManager) Kill(cmd *exec.Cmd) error {
	// Phase 1: TerminateJobObject for force kill
	if cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return nil
}
