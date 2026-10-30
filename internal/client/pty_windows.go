package client

import (
	"io"
	"os"
	"os/exec"
)

// Windows uses pipes for basic prompt-and-response programs; it does not emulate a terminal.
func startInteractive(cmd *exec.Cmd) (io.WriteCloser, io.ReadCloser, error) {
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		input.Close()
		return nil, nil, err
	}
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err = cmd.Start(); err != nil {
		input.Close()
		reader.Close()
		writer.Close()
		return nil, nil, err
	}
	writer.Close()
	return input, reader, nil
}
