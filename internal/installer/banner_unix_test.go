//go:build !windows

package installer

import (
	"agenterm/internal/theme"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"
)

func TestShellInstallerBannerUsesPrimaryColor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "install.sh")
	script, err := Script("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, script, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte("#!/bin/sh\nexit 17\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", path, "preview", "http://127.0.0.1:1/downloads/v1")
	cmd.Env = append(os.Environ(), "AGENTERM_LANG=zh-CN", "TERM=xterm", "COLORTERM=truecolor", "NO_COLOR=", "PATH="+dir+":"+os.Getenv("PATH"), "HOME="+dir, "XDG_CACHE_HOME="+dir)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	output, _ := io.ReadAll(terminal)
	err = cmd.Wait()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 17 {
		t.Fatalf("expected the stub download to fail: %v\n%s", err, output)
	}
	primaryMark := "\x1b[" + theme.PrimarySGR() + "m  " + theme.TerminalLogo()[0]
	if !strings.Contains(string(output), primaryMark) {
		t.Fatalf("banner did not use the configured primary color: %q", output)
	}
}
