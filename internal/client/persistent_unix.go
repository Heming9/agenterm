//go:build !windows

package client

import (
	"agenterm/internal/protocol"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func persistentShellPath() string { return getShell().path }

func persistentShellCommand(ctx context.Context, marker string, options protocol.ExecOptions) *exec.Cmd {
	shell := getShell()
	name := filepath.Base(shell.path)
	args := []string{"-i"}
	switch name {
	case "bash":
		args = []string{"--noprofile", "--norc", "-i"}
	case "zsh":
		args = []string{"-f", "-d", "-i", "-o", "nozle", "-o", "nopromptcr", "-o", "nopromptsp"}
	case "fish":
		args = []string{"--no-config", "-i", "--init-command", "function fish_prompt; set -l agenterm_status $status; printf " + shellQuote(marker+"%s\x1f") + " $agenterm_status; end; function fish_right_prompt; end"}
	}
	cmd := exec.CommandContext(ctx, shell.path, args...)
	cmd.Dir = options.Cwd
	cmd.Env = os.Environ()
	for key, value := range options.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	// User rc files and prompt hooks can print arbitrary text and replace the
	// integration. Inherited environment remains available, but rc files are not
	// sourced. Prompt variables are reserved by the command protocol.
	cmd.Env = append(cmd.Env, "PS1=", "PS2=", "PROMPT_COMMAND=", "ENV=", "BASH_ENV=", "BASH_SILENCE_DEPRECATION_WARNING=1", "PROMPT_EOL_MARK=")
	return cmd
}

func startPersistentInteractive(cmd *exec.Cmd) (io.WriteCloser, io.ReadCloser, error) {
	input, output, err := startInteractive(cmd)
	if err != nil {
		return nil, nil, err
	}
	f := input.(*os.File)
	state, err := terminalState(int(f.Fd()))
	if err == nil {
		state.Lflag &^= unix.ECHO | unix.ECHONL
		err = setTerminalState(int(f.Fd()), state)
	}
	if err != nil {
		_ = input.Close()
		_ = output.Close()
		_ = cmd.Cancel()
		_ = cmd.Wait()
		return nil, nil, err
	}
	return input, output, nil
}

func persistentCommandLine(dir, command string) (string, error) {
	path, err := writeCommandScript(dir, command, ".sh")
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(getShell().path, "/fish") {
		return "source " + shellQuote(path) + "\n", nil
	}
	return ". " + shellQuote(path) + "\n", nil
}

func persistentInitLine(marker string) string {
	name := filepath.Base(getShell().path)
	if name == "fish" {
		return ""
	}
	if name == "zsh" {
		prompt := strings.NewReplacer("\x1e", "\\036", "\x1f", "\\037").Replace(marker + "%?\x1f")
		return "unset PS1 PS2 PROMPT_COMMAND; PS1=$(printf '%b' " + shellQuote(prompt) + "); PS2=''\n"
	}
	// Dash automatically exports PS1 even after unset. Keep the random prefix
	// in an unexported ordinary variable, and expand the whole frame only when
	// that variable exists. Nested shells inherit a harmless empty prompt.
	prefix := strings.ReplaceAll(marker, "\x1e", "\\036")
	prompt := "${agenterm_prompt:+${agenterm_prompt}${?}\\037}"
	return "unset PS1 PS2 PROMPT_COMMAND agenterm_prompt; agenterm_prompt=$(printf '%b' " + shellQuote(prefix) + "); PS1=$(printf '%b' " + shellQuote(prompt) + "); PS2=''\n"
}
