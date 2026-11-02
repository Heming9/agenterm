//go:build !windows

package client

import (
	"agenterm/internal/i18n"
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func startInteractive(cmd *exec.Cmd) (io.WriteCloser, io.ReadCloser, error) {
	f, err := pty.Start(cmd)
	if err != nil {
		return nil, nil, err
	}
	// Rewrap a nonblocking descriptor so Go's poller can unblock both reads and
	// writes when stop closes the PTY, including a program not reading stdin.
	fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err == nil {
		err = unix.SetNonblock(fd, true)
	}
	_ = f.Close()
	if err != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		if cmd.Cancel != nil {
			_ = cmd.Cancel()
		} else {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return nil, nil, err
	}
	f = os.NewFile(uintptr(fd), "agenterm-pty")
	return f, f, nil
}

func interactiveControls() []string { return []string{"interrupt", "eof"} }

func controlInteractive(input io.WriteCloser, action string) (bool, error) {
	f, ok := input.(*os.File)
	if !ok {
		return false, i18n.NewError("session.control.unsupported")
	}
	// File.SyscallConn keeps the descriptor alive against concurrent Close and
	// prevents a recycled descriptor from receiving an unrelated process signal.
	raw, err := f.SyscallConn()
	if err != nil {
		return false, err
	}
	var operationErr error
	var eofByte byte
	err = raw.Control(func(fd uintptr) {
		switch action {
		case "interrupt":
			var group int
			group, operationErr = unix.IoctlGetInt(int(fd), unix.TIOCGPGRP)
			if operationErr == nil {
				if group <= 0 {
					operationErr = i18n.NewError("session.control.no.foreground")
				} else {
					operationErr = unix.Kill(-group, unix.SIGINT)
				}
			}
		case "eof":
			var state *unix.Termios
			state, operationErr = terminalState(int(fd))
			if operationErr == nil {
				if state.Lflag&unix.ICANON == 0 || state.Cc[unix.VEOF] == disabledTerminalChar {
					operationErr = i18n.NewError("session.eof.requires.canonical")
				} else {
					eofByte = state.Cc[unix.VEOF]
				}
			}
		default:
			operationErr = i18n.NewError("session.control.unsupported")
		}
	})
	if err != nil {
		return false, err
	}
	// EOF on a PTY is a terminal input event, not a permanent stdin close.
	if operationErr == nil && action == "eof" {
		_, operationErr = f.Write([]byte{eofByte})
	}
	return false, operationErr
}
