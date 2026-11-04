//go:build linux || darwin

package client

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPersistentShellAdapters(t *testing.T) {
	original := getShell()
	t.Cleanup(func() { selectedShell = original })
	for _, name := range []string{"sh", "bash", "zsh", "dash", "ksh", "fish"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		t.Run(name, func(t *testing.T) {
			selectedShell = path
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			dir := t.TempDir()
			marker := "\x1eadapter-boundary:"
			cmd := persistentShellCommand(ctx, marker, ExecOptions{})
			configureCommand(cmd, true)
			input, output, err := startPersistentInteractive(cmd)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer output.Close()
			defer func() { cancel(); _ = cmd.Wait() }()
			var mu sync.Mutex
			var text strings.Builder
			codes := make(chan int, 8)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				frames := &promptFrames{prefix: []byte(marker), output: func(p []byte) error { mu.Lock(); text.Write(p); mu.Unlock(); return nil }, complete: func(code int) { codes <- code }}
				writer := newOutputWriter(frames)
				_, _ = io.Copy(writer, output)
				_ = writer.Close()
			}()
			defer func() { input.Close(); output.Close(); <-finished }()
			if line := persistentInitLine(marker); line != "" {
				if _, err := io.WriteString(input, line); err != nil {
					t.Fatal(err)
				}
			}
			wait := func(want int) {
				t.Helper()
				select {
				case code := <-codes:
					if code != want {
						t.Fatalf("exit=%d want=%d", code, want)
					}
				case <-ctx.Done():
					mu.Lock()
					defer mu.Unlock()
					t.Fatalf("missing prompt: %q", text.String())
				}
			}
			wait(0)
			commands := []string{"keep=preserved; false", "printf '%s' \"$keep\""}
			if name == "fish" {
				commands = []string{"set keep preserved; false", "printf '%s' $keep"}
			}
			for i, command := range commands {
				line, err := persistentCommandLine(dir, command)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(input, line); err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					wait(1)
				} else {
					wait(0)
				}
			}
			line, err := persistentCommandLine(dir, "/bin/sh -i")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(input, line); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-codes:
				t.Fatalf("nested shell inherited the parent prompt frame: exit=%d", code)
			case <-time.After(100 * time.Millisecond):
			}
			if _, err := io.WriteString(input, "printf '%s' nested; exit\n"); err != nil {
				t.Fatal(err)
			}
			wait(0)
			mu.Lock()
			got := text.String()
			mu.Unlock()
			if !strings.Contains(got, "preserved") || !strings.Contains(got, "nested") {
				t.Fatalf("state lost: %q", got)
			}
		})
	}
}
