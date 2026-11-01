package client

import (
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func configureCommand(cmd *exec.Cmd, _ bool) {
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		kill := exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
		if err := kill.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
