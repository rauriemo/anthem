//go:build !windows

package claude

import (
	"os/exec"
	"syscall"
)

// UnixProcessManager uses process groups to manage process trees.
type UnixProcessManager struct{}

func NewPlatformProcessManager() *UnixProcessManager {
	return &UnixProcessManager{}
}

func (u *UnixProcessManager) Start(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

func (u *UnixProcessManager) Terminate(cmd *exec.Cmd) error {
	if cmd.Process != nil {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	return nil
}

func (u *UnixProcessManager) Kill(cmd *exec.Cmd) error {
	if cmd.Process != nil {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return nil
}
