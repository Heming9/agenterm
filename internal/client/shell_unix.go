//go:build !windows

package client

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func defaultShell() commandShell { return commandShell{"/bin/sh", "-c"} }

func parentShellPath() string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(os.Getppid()), "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(string(out))
	if shellFlag(path) == "" {
		return ""
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return ""
	}
	return resolved
}
