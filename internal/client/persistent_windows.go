package client

import (
	"agenterm/internal/protocol"
	"context"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode/utf16"
)

// PowerShell is used as the persistent driver on Windows, including when the
// launcher was cmd.exe. It supports dot-sourcing and an explicit prompt hook;
// cmd.exe cannot reliably report command boundaries through its prompt.
func persistentShellPath() string {
	path := getShell().path
	if strings.Contains(strings.ToLower(path), "powershell") || strings.Contains(strings.ToLower(path), "pwsh") {
		return path
	}
	if resolved, err := exec.LookPath("powershell.exe"); err == nil {
		return resolved
	}
	return "powershell.exe"
}

func persistentShellCommand(ctx context.Context, marker string, options protocol.ExecOptions) *exec.Cmd {
	path := persistentShellPath()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := "function global:prompt { $agenterm_ok = $?; $agenterm_code = if ($agenterm_ok) { 0 } elseif ($global:LASTEXITCODE) { $global:LASTEXITCODE } else { 1 }; [Console]::Write(" + quote(marker) + " + $agenterm_code + [char]31); return ' ' }"
	var encoded []byte
	for _, c := range utf16.Encode([]rune(script)) {
		encoded = append(encoded, byte(c), byte(c>>8))
	}
	cmd := exec.CommandContext(ctx, path, "-NoLogo", "-NoProfile", "-NoExit", "-EncodedCommand", base64.StdEncoding.EncodeToString(encoded))
	cmd.Dir, cmd.Env = options.Cwd, os.Environ()
	for key, value := range options.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd
}

func startPersistentInteractive(cmd *exec.Cmd) (io.WriteCloser, io.ReadCloser, error) {
	return startInteractive(cmd)
}

func persistentCommandLine(dir, command string) (string, error) {
	path, err := writeCommandScript(dir, "\ufeff"+command, ".ps1")
	return ". ([scriptblock]::Create([System.IO.File]::ReadAllText('" + strings.ReplaceAll(path, "'", "''") + "')))\n", err
}

func persistentInitLine(string) string { return "" }
