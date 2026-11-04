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
	script := "$ProgressPreference = 'SilentlyContinue'; $global:agenterm_utf8 = [System.Text.UTF8Encoding]::new($false); " +
		"[Console]::InputEncoding = $agenterm_utf8; [Console]::OutputEncoding = $agenterm_utf8; $OutputEncoding = $agenterm_utf8; " +
		"$global:agenterm_input = [Console]::OpenStandardInput(); " +
		// Console.ReadLine buffers ahead, stealing queued input from Read-Host or
		// a native child's inherited stdin. Consume only the host command line.
		"function global:PSConsoleHostReadLine { " +
		"$agenterm_bytes = [System.Collections.Generic.List[byte]]::new(); " +
		"while (($agenterm_byte = $global:agenterm_input.ReadByte()) -ge 0) { if ($agenterm_byte -eq 10) { break }; $agenterm_bytes.Add([byte]$agenterm_byte) }; " +
		"if ($agenterm_byte -lt 0 -and $agenterm_bytes.Count -eq 0) { exit }; " +
		"$global:agenterm_command_code = $null; $global:agenterm_error_before = $Error[0]; " +
		"return $global:agenterm_utf8.GetString($agenterm_bytes.ToArray()).TrimEnd([char]13) }; " +
		"function global:prompt { $agenterm_ok = $?; $agenterm_code = if ($null -ne $global:agenterm_command_code) { $global:agenterm_command_code } elseif ($agenterm_ok) { 0 } else { 1 }; return (" + quote(marker) + " + $agenterm_code + [char]31) }"
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
	// Windows PowerShell resets $? when the dot-sourced scriptblock returns.
	// Capture it inside the script, preserving LASTEXITCODE for the user's next
	// command while distinguishing new PowerShell errors from native failures.
	footer := "\n\n$agenterm_command_ok = $?\n" +
		"$global:agenterm_command_code = if ($agenterm_command_ok) { 0 } " +
		"elseif ($Error.Count -and $Error[0] -ne $global:agenterm_error_before -and $Error[0].FullyQualifiedErrorId -notlike 'NativeCommandError*') { 1 } " +
		"elseif ($global:LASTEXITCODE) { $global:LASTEXITCODE } else { 1 }"
	path, err := writeCommandScript(dir, "\ufeff"+command+footer, ".ps1")
	return ". ([scriptblock]::Create([System.IO.File]::ReadAllText('" + strings.ReplaceAll(path, "'", "''") + "')))\n", err
}

func persistentInitLine(string) string { return "" }
