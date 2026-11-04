//go:build !windows

package client

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchingShell(t *testing.T) {
	if os.Getenv("AGENTERM_SHELL_TEST_CHILD") == "1" {
		if filepath.Base(Shell()) != "bash" {
			t.Fatalf("selected shell: %q", Shell())
		}
		manager := NewManager(io.Discard, TerminalCallbacks{})
		defer func() { manager.Close(); manager.WaitCallbacks() }()
		out, err := manager.Execute(context.Background(), ExecuteRequest{Command: `[[ abc == a* ]] && printf matched`})
		if err != nil || out.Output != "matched" {
			t.Fatalf("bash command: output %q, error %v", out.Output, err)
		}
		return
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, "-c", `"$1" -test.run='^TestLaunchingShell$'; result=$?; exit "$result"`, "bash", exe)
	cmd.Env = append(os.Environ(), "AGENTERM_SHELL_TEST_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child failed: %v\n%s", err, strings.TrimSpace(string(out)))
	}
}
