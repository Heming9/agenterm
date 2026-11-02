package service

import (
	"errors"
	"os"
	"os/exec"
)

// Windows target clients remain supported. The split hosting services require
// Unix socket ownership/permissions and currently target Linux and macOS.
func ownsSocketDirectory(os.FileInfo) bool { return false }

func detachRuntime(*exec.Cmd) error {
	return errors.New("automatic runtime hosting requires Linux/macOS")
}

func runtimeProcessAlive(int) bool { return false }
