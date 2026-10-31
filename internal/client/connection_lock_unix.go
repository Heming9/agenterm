//go:build !windows

package client

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryConnectionLock(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func connectionLockBusy(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}
