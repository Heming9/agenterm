//go:build !windows

package service

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func ownsSocketDirectory(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

func detachRuntime(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}

func runtimeProcessAlive(pid int) bool {
	return pid > 0 && !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
