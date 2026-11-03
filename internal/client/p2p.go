package client

import (
	"context"
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
