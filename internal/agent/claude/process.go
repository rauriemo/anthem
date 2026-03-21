package claude

import "os/exec"

type ProcessManager interface {
	Start(cmd *exec.Cmd) error
	Terminate(cmd *exec.Cmd) error
	Kill(cmd *exec.Cmd) error
}
