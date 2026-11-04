package client

import (
	"agenterm/internal/protocol"
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPersistentWindowsUnicodeStateAndInput(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell is not installed")
	}
	original := getShell()
	selectedShell = commandShell{path, "-c"}
	t.Cleanup(func() { selectedShell = original })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const marker = "\x1ewindows-output-test:"
	cmd := persistentShellCommand(ctx, marker, protocol.ExecOptions{})
	configureCommand(cmd, true)
	input, output, err := startPersistentInteractive(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	defer func() { input.Close(); cancel(); _ = cmd.Wait() }()
	var mu sync.Mutex
	var text strings.Builder
	codes := make(chan int, 16)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		frames := &promptFrames{prefix: []byte(marker), output: func(p []byte) error {
			mu.Lock()
			defer mu.Unlock()
			_, err := text.Write(p)
			return err
		}, complete: func(code int) { codes <- code }}
		writer := newOutputWriter(frames)
		_, _ = io.Copy(writer, output)
		_ = writer.Close()
		frames.flush()
	}()
	defer func() { output.Close(); <-finished }()
	wait := func(want int) {
		t.Helper()
		select {
		case code := <-codes:
			if code != want {
				t.Fatalf("exit code = %d, want %d", code, want)
			}
		case <-ctx.Done():
			t.Fatal("missing command completion")
		}
	}
	wait(0)
	dir := t.TempDir()
	run := func(command, stdin string, code int) {
		t.Helper()
		line, err := persistentCommandLine(dir, command)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(input, line+stdin); err != nil {
			t.Fatal(err)
		}
		wait(code)
	}
	run("$bb='中文🙂'; function Get-BB { '函数🙂' }; Write-Output $bb; [Console]::Error.WriteLine('错误🙂')", "", 0)
	run("Write-Output $bb; Get-BB", "", 0)
	run("$bbLine=[Console]::ReadLine(); Write-Output $bbLine", "输入🙂\n", 0)
	mu.Lock()
	got := text.String()
	mu.Unlock()
	const want = "中文🙂\r\n错误🙂\r\n中文🙂\r\n函数🙂\r\n输入🙂\r\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	run("$bbHost=Read-Host; Write-Output ('host-input=' + $bbHost)", "提示🙂\n", 0)
	run("Write-Error 'expected-error'", "", 1)
	run("& $env:ComSpec /d /c 'exit 23'", "", 23)
	run("Write-Output ('last-native=' + $LASTEXITCODE)", "", 0)
	run("Write-Error 'error-after-native'", "", 1)
	run("throw 'expected-throw'", "", 1)
	run("Write-Output 'after-error🙂'", "", 0)
	mu.Lock()
	got = text.String()
	mu.Unlock()
	if !strings.Contains(got, "host-input=提示🙂") || !strings.Contains(got, "expected-error") || !strings.Contains(got, "last-native=23") || !strings.Contains(got, "expected-throw") || !strings.HasSuffix(got, "after-error🙂\r\n") || strings.Contains(got, "CLIXML") {
		t.Fatalf("error or recovery output = %q", got)
	}
	line, err := persistentCommandLine(dir, "& sort.exe; Write-Output 'after-eof'")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(input, line+"beta\r\nalpha\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	wait(0)
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("shell did not exit after EOF")
	}
	mu.Lock()
	got = text.String()
	mu.Unlock()
	if !strings.Contains(got, "alpha\r\nbeta\r\n") || !strings.HasSuffix(got, "after-eof\r\n") {
		t.Fatalf("native stdin/EOF output = %q", got)
	}
}
