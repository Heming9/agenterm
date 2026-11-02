package service

import (
	remoteclient "agenterm/internal/client"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

func callExecution(t *testing.T, session *mcp.ClientSession, args map[string]any) (protocol.CommandResult, bool) {
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
	var out protocol.CommandResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != string(data) {
		// JSON object key order may differ after SDK decoding.
		var textOut protocol.CommandResult
		if len(result.Content) != 1 || json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &textOut) != nil || textOut != out {
			t.Fatalf("structured and text results differ: %+v", result)
		}
	}
	return out, result.IsError
}

func TestExecutionOptionsThroughOfficialMCPClient(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	session := connectMCPClient(t, h, token)
	dir := filepath.Join(t.TempDir(), "working directory")
	// Use an existing directory containing spaces to catch shell interpolation.
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTERM_EXEC_TEST", "inherited")
	command := `printf '%s\n' "$AGENTERM_EXEC_TEST"; pwd; printf 'stderr only\n' >&2; exit 7`
	if runtime.GOOS == "windows" {
		command = `echo %AGENTERM_EXEC_TEST% & cd & echo stderr only 1>&2 & exit /b 7`
	}
	out, isError := callExecution(t, session, map[string]any{
		"command": command, "cwd": dir, "env": map[string]string{"AGENTERM_EXEC_TEST": "overridden"},
	})
	if !isError || out.ExitCode != 7 || out.TimedOut || out.Canceled || out.Truncated || out.Error != "" ||
		!strings.Contains(out.Stdout, "overridden") || !strings.Contains(out.Stdout, dir) ||
		strings.Contains(out.Stdout, "stderr only") || strings.TrimSpace(out.Stderr) != "stderr only" {
		t.Fatalf("execution options/result: %+v isError=%v", out, isError)
	}
	// Directory and environment apply to this command only.
	command = `printf '%s\n' "$AGENTERM_EXEC_TEST"; pwd`
	if runtime.GOOS == "windows" {
		command = `echo %AGENTERM_EXEC_TEST% & cd`
	}
	out, isError = callExecution(t, session, map[string]any{"command": command, "timeout_ms": 0})
	if isError || out.ExitCode != 0 || !strings.Contains(out.Stdout, "inherited") || strings.Contains(out.Stdout, dir) {
		t.Fatalf("command state leaked: %+v", out)
	}
	out, isError = callExecution(t, session, map[string]any{"command": "echo never", "cwd": filepath.Join(dir, "missing")})
	if !isError || out.ExitCode != -1 || out.Error == "" || strings.Contains(out.Stdout, "never") {
		t.Fatalf("missing cwd was not reported: %+v", out)
	}
}

func longCommand() string {
	if runtime.GOOS == "windows" {
		return `echo ready & ping -n 31 127.0.0.1 >nul & echo late`
	}
	return `printf 'ready\n'; sleep 30; printf 'late\n'`
}

func TestExecutionTimeoutAndSessionCancellation(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	events := connectExecutionClient(t, h, token)
	session := connectMCPClient(t, h, token)
	start := time.Now()
	out, isError := callExecution(t, session, map[string]any{"command": longCommand(), "timeout_ms": 1000})
	if !isError || !out.TimedOut || out.Canceled || out.ExitCode != -1 || !strings.Contains(out.Stdout, "ready") || strings.Contains(out.Stdout, "late") || time.Since(start) > 6*time.Second {
		t.Fatalf("timeout: %+v elapsed=%s", out, time.Since(start))
	}
	// HTTP request cancellation must reach the process via the MCP SDK.
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "execute_command", Arguments: map[string]any{"command": longCommand(), "timeout_ms": 0}})
		finished <- err
	}()
	for {
		select {
		case e := <-events:
			if !e.Done && e.Command == longCommand() && e.StartedAt.After(start.Add(time.Second)) {
				cancel()
				goto canceled
			}
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("cancel command did not start")
		}
	}
canceled:
	if err := <-finished; err == nil {
		t.Fatal("canceled MCP call succeeded")
	}
	for {
		select {
		case e := <-events:
			if e.Done {
				if e.ExitCode != -1 {
					t.Fatalf("cancel exit: %+v", e)
				}
				goto stopped
			}
		case <-time.After(5 * time.Second):
			t.Fatal("MCP cancellation did not stop the process")
		}
	}
stopped:
	// Interactive commands keep their own process timeout and stop semantics.
	started := h.call(t, token, "session_start", map[string]any{"command": longCommand(), "timeout_ms": 1000}, false).(map[string]any)
	id := started["session_id"].(string)
	var cursor float64
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := h.call(t, token, "session_read", map[string]any{"session_id": id, "cursor": cursor, "wait_ms": 1000}, false).(map[string]any)
		cursor = r["next_cursor"].(float64)
		if r["done"] == true {
			if r["timed_out"] != true || r["canceled"] != false || r["exit_code"] != float64(-1) {
				t.Fatal(r)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session timeout did not finish")
		}
	}
	started = h.call(t, token, "session_start", map[string]any{"command": longCommand()}, false).(map[string]any)
	id = started["session_id"].(string)
	h.call(t, token, "session_control", map[string]any{"action": "stop", "session_id": id}, false)
	deadline = time.Now().Add(5 * time.Second)
	for {
		r := h.call(t, token, "session_read", map[string]any{"session_id": id, "wait_ms": 1000}, false).(map[string]any)
		if r["done"] == true {
			if r["canceled"] != true || r["timed_out"] != false || r["exit_code"] != float64(-1) {
				t.Fatal(r)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session_control(action=stop) did not finish")
		}
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
			if m.Type != "exec" {
				continue
			}
			for _, output := range []protocol.Message{
				{Type: "output", ID: m.ID, Stream: "stdout", Output: strings.Repeat("界", maxCommandOutput/3+3)},
				{Type: "output", ID: m.ID, Stream: "stderr", Output: "error"},
				{Type: "done", ID: m.ID, ExitCode: 7},
			} {
				if ws.WriteJSON(output) != nil {
					return
				}
			}
		}
	}()
	session := connectMCPClient(t, h, token)
	out, isError := callExecution(t, session, map[string]any{"command": "large"})
	if !isError || !out.Truncated || out.ExitCode != 7 || len(out.Stdout)+len(out.Stderr) > maxCommandOutput || !utf8.ValidString(out.Stdout) || len(out.Stdout) < maxCommandOutput-3 {
		t.Fatalf("truncated result: stdout=%d stderr=%d exit=%d truncated=%v", len(out.Stdout), len(out.Stderr), out.ExitCode, out.Truncated)
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
		if tool.Name == "execute_command" && tool.OutputSchema == nil {
			t.Fatal("missing execution output schema")
		}
		if tool.Name == "execute_command" || tool.Name == "session_start" {
			data, _ := json.Marshal(tool.InputSchema)
			var schema struct {
				Properties map[string]struct{ Default int64 }
			}
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatal(err)
			}
			want := int64(0)
			if tool.Name == "execute_command" {
				want = protocol.DefaultCommandTimeoutMS
			}
			if schema.Properties["timeout_ms"].Default != want {
				t.Fatalf("%s timeout default: %s", tool.Name, data)
			}
		}
		if tool.Name == "create_client" {
			t.Fatal("device credential can discover management tools")
		}
	}
	for _, args := range []map[string]any{
		{"command": "echo never", "timeout_ms": -1},
		{"command": "echo never", "timeout_ms": protocol.MaxCommandTimeoutMS + 1},
		{"command": "echo never", "timeout_ms": 1.5},
		{"command": "echo never", "env": map[string]any{"X": 1}},
		{"command": "echo never", "env": map[string]string{"A=B": "bad"}},
		{"command": "echo never", "cwd": "bad\x00dir"},
		{"command": "   "},
	} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "execute_command", Arguments: args})
		if err != nil || !result.IsError {
			t.Fatalf("invalid arguments accepted: %v result=%+v err=%v", args, result, err)
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
	command := `IFS= read -r AGENTERM_REPLY; printf 'received:%s:%s\n' "$AGENTERM_REPLY" "$AGENTERM_EXEC_TEST"; pwd`
	want := "received:early-input:session-env"
	if runtime.GOOS == "windows" {
		command = `set /p AGENTERM_REPLY= & set AGENTERM_REPLY & set AGENTERM_EXEC_TEST & cd`
		want = "AGENTERM_REPLY=early-input"
	}
	start := h.call(t, token, "session_start", map[string]any{
		"command": command, "cwd": dir, "env": map[string]string{"AGENTERM_EXEC_TEST": "session-env"}, "timeout_ms": 4000,
	}, false).(map[string]any)
	id := start["session_id"].(string)
	// Send input immediately, without waiting for process startup or a prompt.
	h.call(t, token, "session_control", map[string]any{"action": "input", "session_id": id, "input": "early-input\n"}, false)
	var output strings.Builder
	var cursor float64
	for {
		r := h.call(t, token, "session_read", map[string]any{"session_id": id, "cursor": cursor, "wait_ms": 1000}, false).(map[string]any)
		output.WriteString(r["output"].(string))
		cursor = r["next_cursor"].(float64)
		if r["done"] == true {
			if r["exit_code"] != float64(0) || r["timed_out"] != false || !strings.Contains(output.String(), want) || !strings.Contains(output.String(), "session-env") || !strings.Contains(output.String(), dir) {
				t.Fatalf("session input/options: %v output=%q", r, output.String())
			}
			break
		}
	}
}
