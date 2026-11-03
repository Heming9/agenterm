package service

import (
	remoteclient "agenterm/internal/client"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type authenticatedTransport struct{ token string }

func (a authenticatedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	return http.DefaultTransport.RoundTrip(r)
}

func connectMCPClient(t *testing.T, h *multiHarness, token string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := mcp.NewClient(&mcp.Implementation{Name: "agenterm-integration", Version: version.Current}, nil)
	session, err := c.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             h.ts.URL + "/agenterm/mcp",
		HTTPClient:           &http.Client{Transport: authenticatedTransport{token}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func connectExecutionClient(t *testing.T, h *multiHarness, token string) <-chan remoteclient.CommandEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	events := make(chan remoteclient.CommandEvent, 32)
	go func() {
		ended <- remoteclient.RunWithCallbacks(ctx, token, io.Discard, remoteclient.Callbacks{
			Command: func(e remoteclient.CommandEvent) { events <- e },
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-ended:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("execution client did not stop")
		}
	})
	decoded, _ := protocol.Decode(token)
	ctxWait, stopWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopWait()
	clients, err := h.s.waitForClients(ctxWait, decoded.Secret, 5*time.Second)
	if err != nil || len(clients) != 1 {
		t.Fatalf("client connection: %v %v", clients, err)
	}
	return events
}

func callExecution(t *testing.T, session *mcp.ClientSession, args map[string]any) (executeResult, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "execute_command", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out executeResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	var textOut executeResult
	if len(result.Content) != 1 || json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &textOut) != nil || !reflect.DeepEqual(textOut, out) {
		t.Fatalf("text/structured mismatch: %+v", result)
	}
	return out, result.IsError
}

func TestExecutionOptionsThroughOfficialMCPClient(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	session := connectMCPClient(t, h, token)
	dir := filepath.Join(t.TempDir(), "working directory")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	command := `printf '%s\n' "$AGENTERM_EXEC_TEST"; pwd; printf 'stderr only\n' >&2; false`
	if runtime.GOOS == "windows" {
		command = `Write-Output $env:AGENTERM_EXEC_TEST; Get-Location; [Console]::Error.WriteLine('stderr only'); cmd /c exit 1`
	}
	out, isError := callExecution(t, session, map[string]any{"terminal_name": "options", "command": command, "cwd": dir, "env": map[string]string{"AGENTERM_EXEC_TEST": "overridden"}})
	if !isError || out.ExitCode == nil || *out.ExitCode != 1 || out.Status != "completed" || !strings.Contains(out.Output, "overridden") || !strings.Contains(out.Output, dir) || !strings.Contains(out.Output, "stderr only") {
		t.Fatalf("result: %+v error=%v", out, isError)
	}
	command = `printf '%s\n' "$AGENTERM_EXEC_TEST"; pwd`
	if runtime.GOOS == "windows" {
		command = `Write-Output $env:AGENTERM_EXEC_TEST; Get-Location`
	}
	reused, isError := callExecution(t, session, map[string]any{"terminal_name": "options", "command": command})
	if isError || reused.SessionID != out.SessionID || reused.SessionCreated || !strings.Contains(reused.Output, "overridden") || !strings.Contains(reused.Output, dir) {
		t.Fatalf("state lost: %+v", reused)
	}
	bad, isError := callExecution(t, session, map[string]any{"command": "echo never", "terminal_name": random(), "cwd": filepath.Join(dir, "missing")})
	if !isError || bad.ExitCode == nil || *bad.ExitCode != -1 || bad.Error == "" || strings.Contains(bad.Output, "never") {
		t.Fatalf("missing cwd: %+v", bad)
	}
}

func longCommand() string {
	if runtime.GOOS == "windows" {
		return `Write-Output ready; Start-Sleep -Seconds 30; Write-Output late`
	}
	return `printf 'ready\n'; sleep 30; printf 'late\n'`
}

func TestExecutionWaitCancellationDoesNotCancelSession(t *testing.T) {
	for _, retain := range []bool{false, true} {
		t.Run(fmt.Sprintf("retain=%t", retain), func(t *testing.T) {
			h := newMultiHarness(t, 0)
			token := h.create(t, false)
			events := connectExecutionClient(t, h, token)
			session := connectMCPClient(t, h, token)
			command, wait := longCommand(), 20000
			if retain {
				command, wait = "sleep 0.4; printf finished", 150
				if runtime.GOOS == "windows" {
					command = "Start-Sleep -Milliseconds 400; Write-Output finished"
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "execute_command", Arguments: map[string]any{"command": command, "wait_ms": wait}})
				finished <- err
			}()
			select {
			case <-events:
				cancel()
			case <-time.After(5 * time.Second):
				t.Fatal("command did not start")
			}
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("canceled waiter returned success")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("waiter did not cancel")
			}
			deadline := time.Now().Add(5 * time.Second)
			var item map[string]any
			for {
				items := h.call(t, token, "session_list", map[string]any{}, false).(map[string]any)["sessions"].([]any)
				if len(items) != 1 {
					t.Fatalf("lost canceled execution: %v", items)
				}
				item = items[0].(map[string]any)
				if !retain || item["command_done"] == true {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("client did not retain after waiter cancellation")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if (!retain && item["status"] != "running") || (retain && (item["persistent"] != true || item["promoted"] != true || item["session_alive"] != true || item["terminal_name"] == "")) {
				t.Fatalf("waiter changed execution lifetime: %v", item)
			}
			stopped := h.call(t, token, "session_control", map[string]any{"action": "stop", "session_id": item["session_id"]}, false).(map[string]any)
			if stopped["done"] != true || stopped["canceled"] != true {
				t.Fatal(stopped)
			}
		})
	}
}

func TestExecutionOutputLimitKeepsExitStatus(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, _ := h.connect(t, token, "large output")
	go func() {
		for {
			var m protocol.Message
			if ws.ReadJSON(&m) != nil {
				return
			}
			if m.Type != "session_exec" {
				continue
			}
			m = acceptTestSession(ws, m)
			for _, reply := range []protocol.Message{
				{Type: "output", ID: m.ID, Output: strings.Repeat("界", maxSessionOutput/3+3)},
				{Type: "command_done", ID: m.ID, RequestID: m.RequestID, ExitCode: 7},
				{Type: "done", ID: m.ID, ExitCode: 7},
			} {
				if ws.WriteJSON(reply) != nil {
					return
				}
			}
		}
	}()
	session := connectMCPClient(t, h, token)
	out, isError := callExecution(t, session, map[string]any{"command": "large"})
	if !isError || !out.Truncated || out.ExitCode == nil || *out.ExitCode != 7 || len(out.Output) > defaultSessionReadBytes || !utf8.ValidString(out.Output) || !out.HasMore {
		t.Fatalf("paged result: %+v", out)
	}
}

func TestMCPExecutionValidationAndSchemas(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	session := connectMCPClient(t, h, token)
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "session_start" || tool.Name == "create_client" {
			t.Fatalf("unexpected tool %s", tool.Name)
		}
		if tool.Name != "execute_command" {
			continue
		}
		if tool.OutputSchema == nil {
			t.Fatal("missing output schema")
		}
		data, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Properties map[string]struct{ Default int64 }
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		if _, exists := schema.Properties["terminal_name"]; !exists {
			t.Fatal("missing terminal_name")
		}
		if _, exists := schema.Properties["new_session"]; exists {
			t.Fatal("retired new_session still published")
		}
		if _, exists := schema.Properties["timeout_ms"]; exists {
			t.Fatal("retired timeout_ms still published")
		}
		if schema.Properties["wait_ms"].Default != 10000 {
			t.Fatal(string(data))
		}
	}
	// An invalid argument must be rejected even with an executable target;
	// otherwise a missing device could hide a silently accepted retired field.
	connectExecutionClient(t, h, token)
	for _, args := range []map[string]any{
		{"command": "echo never", "timeout_ms": 0},
		{"command": "echo never", "new_session": true},
		{"command": "echo never", "terminal_name": ""},
		{"command": "echo never", "terminal_name": "  "},
		{"command": "echo never", "terminal_name": "bad\nname"},
		{"command": "echo never", "terminal_name": strings.Repeat("x", 65)},
		{"command": "echo never", "env": map[string]any{"X": 1}},
		{"command": "echo never", "env": map[string]string{"A=B": "bad"}},
		{"command": "echo never", "cwd": "bad\x00dir"},
		{"command": "   "},
		{"command": "echo never", "terminal_name": random(), "session_id": "other"},
		{"command": "echo never", "wait_ms": -1},
		{"command": "echo never", "wait_ms": 20001},
	} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "execute_command", Arguments: args})
		if err != nil || !result.IsError {
			t.Fatalf("invalid args: %v %v %v", args, result, err)
		}
	}
}

func TestExecutionSessionInputAndOptions(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	dir := filepath.Join(t.TempDir(), "session directory")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	command := `printf 'ready\n'; IFS= read -r AGENTERM_REPLY; printf 'received:%s:%s\n' "$AGENTERM_REPLY" "$AGENTERM_EXEC_TEST"; pwd`
	want := "received:early-input:session-env"
	if runtime.GOOS == "windows" {
		command = `Write-Output ready; $reply=Read-Host; Write-Output "received:${reply}:$env:AGENTERM_EXEC_TEST"; Get-Location`
	}
	started := h.call(t, token, "execute_command", map[string]any{"terminal_name": random(), "wait_ms": 0, "command": command, "cwd": dir, "env": map[string]string{"AGENTERM_EXEC_TEST": "session-env"}}, false).(map[string]any)
	id := started["session_id"]
	if started["status"] != "running" || started["command_done"] != false {
		t.Fatal(started)
	}
	busy := h.call(t, token, "execute_command", map[string]any{"session_id": id, "command": "echo must-not-run"}, true).(map[string]any)
	if !strings.Contains(busy["error"].(string), "session_busy") || !strings.Contains(busy["error"].(string), id.(string)) {
		t.Fatal(busy)
	}
	h.call(t, token, "session_control", map[string]any{"action": "input", "session_id": id, "input": "early-input\n"}, false)
	output, completed := readExecutionToCompletion(t, h, token, started)
	if completed["exit_code"] != float64(0) || completed["timed_out"] != false || completed["done"] != false || completed["session_alive"] != true || !strings.Contains(output, "ready") || !strings.Contains(output, want) || !strings.Contains(output, dir) {
		t.Fatalf("input/options: %v output=%q", completed, output)
	}
	h.call(t, token, "execute_command", map[string]any{"session_id": id, "command": "echo next"}, false)
	h.call(t, token, "session_read", map[string]any{"session_id": id, "command_id": started["command_id"]}, true)
	stopped := h.call(t, token, "session_control", map[string]any{"session_id": id, "action": "stop"}, false).(map[string]any)
	if stopped["done"] != true || stopped["canceled"] != true {
		t.Fatal(stopped)
	}
	h.call(t, token, "execute_command", map[string]any{"session_id": id, "command": "echo closed"}, true)
	fresh := h.call(t, token, "execute_command", map[string]any{"command": "echo fresh"}, false).(map[string]any)
	if fresh["session_created"] != true || fresh["session_id"] == id {
		t.Fatal(fresh)
	}
}
