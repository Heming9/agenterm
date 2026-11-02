//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package client

import (
	"agenterm/internal/i18n"
	"golang.org/x/sys/unix"
)

const disabledTerminalChar = 0

func terminalState(int) (*unix.Termios, error) {
	return nil, i18n.NewError("session.control.unsupported")
}
