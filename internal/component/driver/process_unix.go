//go:build unix

package driver

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// A compiler or package manager may start other tools. Cancel the whole
// group so they release the work directory and close the command's pipes.
func isolateTool(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}

		return err
	}
}
