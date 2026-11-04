package client

import (
	"agenterm/internal/protocol"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
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
	// Pipe traffic is UTF-8 in both directions, independent of the user's code
	// page. Read host commands directly so PowerShell does not echo our internal
	// dot-source line. Programs can still read their own input from the same pipe.
	script := "$ProgressPreference = 'SilentlyContinue'; $agenterm_utf8 = [System.Text.UTF8Encoding]::new($false); " +
		"[Console]::InputEncoding = $agenterm_utf8; [Console]::OutputEncoding = $agenterm_utf8; $OutputEncoding = $agenterm_utf8; " +
		"function global:PSConsoleHostReadLine { $agenterm_line = [Console]::ReadLine(); if ($null -eq $agenterm_line) { exit }; return $agenterm_line }; " +
		"function global:prompt { $agenterm_ok = $?; $agenterm_code = if ($agenterm_ok) { 0 } elseif ($global:LASTEXITCODE) { $global:LASTEXITCODE } else { 1 }; return (" + quote(marker) + " + $agenterm_code + [char]31) }"
	// EncodedCommand selects CLIXML for stderr in Windows PowerShell. A normal
	// command keeps genuine errors as text without serialized progress records.
	cmd := exec.CommandContext(ctx, path, "-NoLogo", "-NoProfile", "-NoExit", "-Command", script)
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
