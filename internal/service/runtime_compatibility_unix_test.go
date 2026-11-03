//go:build !windows

package service

import (
	"agenterm/internal/version"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIncompatibleRuntimeCommandsPreserveRunningState(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ag-compat-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMPDIR", dir)
	paths, err := pathsForInstance("staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.prepare(); err != nil {
		t.Fatal(err)
	}
	// A published backend must not be contacted, replaced or unlinked.
	if err := os.WriteFile(paths.Server, []byte("old server endpoint"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", paths.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	runtime := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/config" {
			t.Errorf("incompatible command attempted more than discovery: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set(controlVersionHeader, "6")
		w.Header().Set(runtimeVersionHeader, "0.4.0")
		w.Header().Set(runtimePIDHeader, strconv.Itoa(os.Getpid()))
		w.WriteHeader(http.StatusUpgradeRequired)
	})}
	go runtime.Serve(listener)
	t.Cleanup(func() { runtime.Close() })

	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, clientID := h.connect(t, token, "retained")
	go serveTestDevice(ws, "retained", nil, nil)
	sessionID := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "command": "interactive"}, false).(map[string]any)["session_id"].(string)
	before := h.call(t, token, "session_read", map[string]any{"session_id": sessionID, "wait_ms": 1000}, false).(map[string]any)
	cursor := before["next_cursor"]

	executable := filepath.Join(dir, "agenterm-server")
	build := exec.Command("go", "build", "-o", executable, "./cmd/agenterm-server")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	for _, action := range []string{"start", "stop", "restart", "status"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		output, err := exec.CommandContext(ctx, executable, action, "-instance", "staging").CombinedOutput()
		cancel()
		if err == nil {
			t.Fatalf("%s unexpectedly succeeded: %s", action, output)
		}
		for _, part := range []string{version.Current, "API " + controlVersion, "0.4.0", "API 6", "runtime PID " + strconv.Itoa(os.Getpid()), "stop -instance staging", "kill -TERM " + strconv.Itoa(os.Getpid())} {
			if !strings.Contains(string(output), part) {
				t.Fatalf("%s missing %q: %s", action, part, output)
			}
		}
		data, err := os.ReadFile(paths.Server)
		if err != nil || string(data) != "old server endpoint" {
			t.Fatalf("%s changed old backend: %s, %v", action, data, err)
		}
		entries, err := os.ReadDir(paths.Dir)
		if err != nil || len(entries) != 2 {
			t.Fatalf("%s left new IPC files: %v, %v", action, entries, err)
		}
		// The same connection, credential, session and cursor remain usable.
		h.call(t, token, "session_control", map[string]any{"action": "input", "session_id": sessionID, "input": action}, false)
		after := h.call(t, token, "session_read", map[string]any{"session_id": sessionID, "cursor": cursor, "wait_ms": 1000}, false).(map[string]any)
		if after["status"] == "closed" || !strings.Contains(after["output"].(string), "retained:"+action) {
			t.Fatalf("%s interrupted the session: %v", action, after)
		}
		cursor = after["next_cursor"]
		clients := h.call(t, token, "list_clients", map[string]any{}, false).(map[string]any)["clients"].([]any)
		if len(clients) != 1 || clients[0].(map[string]any)["client_id"] != clientID {
			t.Fatalf("%s changed existing client connection: %v", action, clients)
		}
	}
	if requests.Load() != 4 {
		t.Fatalf("commands should only discover runtime once each: %d", requests.Load())
	}
	// The same live runtime socket remains reachable after all refusals.
	connection, err := net.DialTimeout("unix", paths.Runtime, time.Second)
	if err != nil {
		t.Fatal("runtime no longer reachable: ", err)
	}
	connection.Close()
}
