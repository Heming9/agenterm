//go:build darwin || freebsd || openbsd || netbsd || dragonfly

package client

import "golang.org/x/sys/unix"

const disabledTerminalChar = 255

func terminalState(fd int) (*unix.Termios, error) {
	return unix.IoctlGetTermios(fd, unix.TIOCGETA)
}

func setTerminalState(fd int, state *unix.Termios) error {
	return unix.IoctlSetTermios(fd, unix.TIOCSETA, state)
}
