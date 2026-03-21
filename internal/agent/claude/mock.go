package claude

import "os/exec"

type MockProcessManager struct {
	StartFunc     func(cmd *exec.Cmd) error
	TerminateFunc func(cmd *exec.Cmd) error
	KillFunc      func(cmd *exec.Cmd) error
}

func NewMockProcessManager() *MockProcessManager {
	return &MockProcessManager{
		StartFunc:     func(cmd *exec.Cmd) error { return cmd.Start() },
		TerminateFunc: func(_ *exec.Cmd) error { return nil },
		KillFunc:      func(_ *exec.Cmd) error { return nil },
	}
}

func (m *MockProcessManager) Start(cmd *exec.Cmd) error {
	return m.StartFunc(cmd)
}

func (m *MockProcessManager) Terminate(cmd *exec.Cmd) error {
	return m.TerminateFunc(cmd)
}

func (m *MockProcessManager) Kill(cmd *exec.Cmd) error {
	return m.KillFunc(cmd)
}
