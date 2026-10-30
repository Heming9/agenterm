//go:build !windows

package client

import (
	"io"
	"os/exec"

	"github.com/creack/pty"
)

func startInteractive(cmd *exec.Cmd) (io.WriteCloser, io.ReadCloser, error) {
	f, err := pty.Start(cmd)
	if err != nil {
		return nil, nil, err
	}
	return f, f, nil
}
