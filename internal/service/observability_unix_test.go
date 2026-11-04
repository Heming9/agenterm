//go:build !windows

package service

import (
	remoteclient "agenterm/internal/client"
	"agenterm/internal/logging"
	"agenterm/internal/protocol"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readLogRecords(t *testing.T, path string, match func(map[string]any) bool) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var found []map[string]any
		for _, line := range bytes.Split(data, []byte("\n")) {
			var record map[string]any
			if json.Unmarshal(line, &record) == nil && match(record) {
				found = append(found, record)
			}
		}
		if len(found) > 0 {
			return found
		}
		if time.Now().After(deadline) {
			t.Fatalf("matching log not written to %s:\n%s", path, data)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRuntimeLogsSurviveServerCrashAndCorrelateMCP(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ag-logs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMPDIR", dir)
	t.Setenv("AGENTERM_LOG_FORMAT", "json")
	t.Setenv("AGENTERM_LOG_LEVEL", "DEBUG")
	t.Setenv("AGENTERM_RUNTIME_LOG_FILE", "")
	serverLog := filepath.Join(dir, "server.jsonl")
	t.Setenv("AGENTERM_LOG_FILE", serverLog)
	paths, err := pathsForInstance("logging")
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	reserved.Close()
	origin := "http://" + address
	args := []string{"-instance", "logging", "-listen", address, "-public-url", origin, "-passwd", "admin-test"}
	first := startService(t, "server", dir, args...)
	t.Cleanup(func() { _ = exec.Command(filepath.Join(dir, "agenterm-server"), "stop", "-instance", "logging").Run() })
	waitReady(t, origin+"/healthz", http.DefaultTransport, first)
	h := &multiHarness{ts: &httptest.Server{URL: origin}}
	token := h.create(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() { ended <- remoteclient.RunWithCallbacks(ctx, token, io.Discard, remoteclient.Callbacks{}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Error("client did not stop")
		}
	})
	h.call(t, token, "list_client", map[string]any{"wait_ms": 4000}, false)
	command := "printf 'private-command-output'"
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "execute_command", "arguments": map[string]any{"command": command, "setup": map[string]any{"env": map[string]string{"PRIVATE_VAR": "private-env"}}},
	}})
	r, _ := http.NewRequest("POST", origin+"/agenterm/mcp", bytes.NewReader(body))
	setMCPHeaders(r)
	r.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	result, _ := io.ReadAll(response.Body)
	response.Body.Close()
	id := response.Header.Get(logging.RequestIDHeader)
	if response.StatusCode != http.StatusOK || !bytes.Contains(result, []byte("private-command-output")) || !logging.ValidRequestID(id) {
		t.Fatalf("execution failed: %d %s", response.StatusCode, result)
	}
	runtimeLog := filepath.Join(paths.Dir, "runtime.log")
	commandLogs := readLogRecords(t, runtimeLog, func(entry map[string]any) bool {
		return entry["msg"] == "command completed" && entry["request_id"] == id
	})
	if commandLogs[0]["component"] != "runtime" || commandLogs[0]["instance"] != "logging" || commandLogs[0]["command_id"] == nil || commandLogs[0]["client_id"] == nil {
		t.Fatal(commandLogs)
	}
	readLogRecords(t, serverLog, func(entry map[string]any) bool {
		return entry["msg"] == "mcp tool completed" && entry["request_id"] == id && entry["tool"] == "execute_command"
	})
	session := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "command": `read value; printf '%s' "$value"`}, false).(map[string]any)
	sessionID := session["session_id"].(string)
	h.call(t, token, "session_control", map[string]any{"session_id": sessionID, "action": "input", "input": "private-session-input\n"}, false)
	sessionLogs := readLogRecords(t, runtimeLog, func(entry map[string]any) bool {
		return entry["msg"] == "command completed" && entry["session_id"] == sessionID
	})
	readLogRecords(t, serverLog, func(entry map[string]any) bool {
		return entry["msg"] == "mcp tool completed" && entry["tool"] == "execute_command" && entry["request_id"] == sessionLogs[0]["request_id"]
	})
	first.stop(t, true)
	response, err = http.Get(origin + "/agenterm/c/private-install-code?secret=private-query")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatal(response.StatusCode)
	}
	proxyID := response.Header.Get(logging.RequestIDHeader)
	readLogRecords(t, runtimeLog, func(entry map[string]any) bool {
		return entry["msg"] == "server proxy failed" && entry["request_id"] == proxyID
	})
	replacement := startService(t, "server", dir, args...)
	waitReady(t, origin+"/healthz", http.DefaultTransport, replacement)
	h.call(t, token, "execute_command", map[string]any{"command": "printf after-restart"}, false)
	readLogRecords(t, runtimeLog, func(entry map[string]any) bool {
		return entry["msg"] == "command completed" && entry["request_id"] != id
	})
	replacement.stop(t, false)
	decoded, _ := protocol.Decode(token)
	for _, path := range []string{serverLog, runtimeLog} {
		data, _ := os.ReadFile(path)
		for _, secret := range []string{"admin-test", token, decoded.Secret, command, "private-command-output", "private-env", "private-install-code", "private-query", "private-session-input"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("secret escaped into %s: %s", path, secret)
			}
		}
	}
}
