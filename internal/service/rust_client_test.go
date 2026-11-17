package service

import (
	"agenterm/internal/components"
	"agenterm/internal/protocol"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

var rustClientDir string
var rustClientTempRoot = os.TempDir()

var rustClientBinary = sync.OnceValues(func() (string, error) {
	// CI can exercise a fresh client build. Ordinary Go tests use the native
	// client from the same component bundle that the server distributes.
	if path := os.Getenv("AGENTERM_RUST_CLI"); path != "" {
		return filepath.Abs(path)
	}
	var err error
	rustClientDir, err = os.MkdirTemp(rustClientTempRoot, "agenterm-test-client-")
	if err != nil {
		return "", err
	}
	paths, err := components.Prepare(rustClientDir)
	if err != nil {
		return "", err
	}
	platform := runtime.GOOS
	if platform == "darwin" {
		platform = "macos"
	}
	name := fmt.Sprintf("agenterm-client-cli-%s-%s", platform, runtime.GOARCH)
	if platform == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(paths.Root, "client-cli", name)
	if platform != "windows" {
		if err := os.Chmod(binary, 0700); err != nil {
			return "", err
		}
	}
	return binary, nil
})

func startRustClient(t *testing.T, token string) func() {
	t.Helper()
	binary, err := rustClientBinary()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	log, err := os.Create(filepath.Join(home, "client.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-plain", "-token", token, "-connection-mode", "persistent")
	cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home,
		"AGENTERM_SKIP_LINK_REGISTRATION=1", "NO_COLOR=1", "SHELL=/bin/sh")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatalf("start Rust client: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		_ = log.Close()
		close(done)
	}()
	stop := sync.OnceFunc(func() {
		select {
		case <-done:
			return
		default:
		}
		if runtime.GOOS == "windows" {
			_ = cmd.Process.Kill()
		} else {
			_ = cmd.Process.Signal(os.Interrupt)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("Rust client did not stop")
		}
	})
	t.Cleanup(func() {
		stop()
		if t.Failed() {
			if file, err := os.Open(log.Name()); err == nil {
				defer file.Close()
				data := make([]byte, 4096)
				n, _ := file.Read(data)
				t.Logf("Rust CLI output (first %d bytes): %s", n, data[:n])
			}
		}
	})
	return stop
}

func waitForRustClient(t *testing.T, h *multiHarness, token string) {
	t.Helper()
	decoded, err := protocol.Decode(token)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clients, err := h.s.waitForClients(ctx, decoded.Secret, 5*time.Second)
	if err != nil || len(clients) != 1 {
		t.Fatalf("Rust client connection: %v %v", clients, err)
	}
}

func connectExecutionClient(t *testing.T, h *multiHarness, token string) {
	t.Helper()
	startRustClient(t, token)
	waitForRustClient(t, h, token)
}
