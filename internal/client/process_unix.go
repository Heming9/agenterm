//go:build !windows

package client

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureCommand(cmd *exec.Cmd, interactive bool) {
	// PTY startup creates a session and process group itself.
	if !interactive {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
