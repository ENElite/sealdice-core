//go:build !windows

package dice

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureJsExecCommand puts the child in its own process group. This makes a
// timeout or JSVM shutdown terminate wrappers and their descendants together;
// otherwise a CLI implemented as a small launcher could leave its real worker
// running after the launcher is killed.
func configureJsExecCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
