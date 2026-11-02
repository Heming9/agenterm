package client

import "golang.org/x/sys/unix"

const disabledTerminalChar = 0

func terminalState(fd int) (*unix.Termios, error) {
	return unix.IoctlGetTermios(fd, unix.TCGETS)
}
