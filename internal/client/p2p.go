package client

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"
)

func p2pCommand(ctx context.Context, script string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", script)
}
func stopP2P(script string) {
	if script == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := p2pCommand(ctx, script)
	configureCommand(command, false)
	_ = command.Run() // Graceful local IPC first; the owner's normal process-tree kill is the fallback.
}

// Bootstrap runs a transport helper without consuming a managed-terminal slot.
type BootstrapOptions struct{ Script, StopScript, ConfigBase64 string }

func Bootstrap(ctx context.Context, a BootstrapOptions, output io.Writer) (int, error) {
	defer stopP2P(a.StopScript)
	cmd := p2pCommand(ctx, a.Script)
	configureCommand(cmd, false)
	cmd.Env = append(os.Environ(), "AGENTERM_P2P_CONFIG_B64="+a.ConfigBase64)
	stdout, stderr := newOutputWriter(output), newOutputWriter(output)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	_ = stdout.Close()
	_ = stderr.Close()
	code, detail := processResult(cmd, err)
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	if detail != "" {
		return code, fmt.Errorf("%s", detail)
	}
	return code, nil
}
