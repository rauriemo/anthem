//go:build windows

package claude

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WindowsProcessManager uses Job Objects to manage process trees.
// Each spawned process is assigned to a Job with KILL_ON_JOB_CLOSE,
// ensuring all descendants are terminated when the job handle is closed
// (explicitly via Terminate/Kill, or implicitly when Anthem exits).
type WindowsProcessManager struct {
	mu   sync.Mutex
	jobs map[*exec.Cmd]windows.Handle
}

func NewPlatformProcessManager() *WindowsProcessManager {
	return &WindowsProcessManager{
		jobs: make(map[*exec.Cmd]windows.Handle),
	}
}

func (w *WindowsProcessManager) Start(cmd *exec.Cmd) error {
	job, jobErr := createJobObject()

	if err := cmd.Start(); err != nil {
		if jobErr == nil {
			windows.CloseHandle(job)
		}
		return err
	}

	if jobErr != nil {
		return nil
	}

	proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		windows.CloseHandle(job)
		return nil
	}

	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		windows.CloseHandle(proc)
		windows.CloseHandle(job)
		return nil
	}
	windows.CloseHandle(proc)

	w.mu.Lock()
	w.jobs[cmd] = job
	w.mu.Unlock()

	return nil
}

func createJobObject() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE

	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		windows.CloseHandle(job)
		return 0, err
	}

	return job, nil
}

func (w *WindowsProcessManager) terminateJob(cmd *exec.Cmd) error {
	w.mu.Lock()
	job, ok := w.jobs[cmd]
	if ok {
		delete(w.jobs, cmd)
	}
	w.mu.Unlock()

	if ok {
		err := windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
		return err
	}

	if cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return nil
}

func (w *WindowsProcessManager) Terminate(cmd *exec.Cmd) error {
	return w.terminateJob(cmd)
}

func (w *WindowsProcessManager) Kill(cmd *exec.Cmd) error {
	return w.terminateJob(cmd)
}
