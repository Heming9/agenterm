package client

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type commandShell struct {
	path string
	flag string
}

var shellOnce sync.Once
var selectedShell commandShell

// Shell reports the shell selected for commands in this client process.
func Shell() string { return persistentShellPath() }

func getShell() commandShell {
	shellOnce.Do(func() {
		selectedShell = defaultShell()
		if path := parentShellPath(); path != "" {
			if flag := shellFlag(path); flag != "" {
				selectedShell = commandShell{path, flag}
			}
		}
	})
	return selectedShell
}

func shellFlag(path string) string {
	name := strings.ToLower(filepath.Base(path))
	name = strings.TrimSuffix(name, ".exe")
	switch name {
	case "cmd":
		return "/C"
	case "powershell", "pwsh":
		return "-c"
	case "sh", "bash", "zsh", "dash", "ksh", "fish", "ash":
		return "-c"
	default:
		return ""
	}
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	shell := getShell()
	return exec.CommandContext(ctx, shell.path, shellArgs(shell, command)...)
}

func shellArgs(shell commandShell, command string) []string {
	args := []string{shell.flag, command}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(shell.path)), ".exe")
	if name == "powershell" || name == "pwsh" {
		args = append([]string{"-NoP"}, args...)
	}
	return args
}
