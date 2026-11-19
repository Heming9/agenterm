package interop

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type authenticatedTransport struct{ token string }

func (a authenticatedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestRustCLIRealServer(t *testing.T) {
	serverPath, clientPath := os.Getenv("AGENTERM_GO_SERVER"), os.Getenv("AGENTERM_RUST_CLI")
	if serverPath == "" || clientPath == "" {
		t.Skip("set AGENTERM_GO_SERVER and AGENTERM_RUST_CLI to enable full runtime/MCP interoperability")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the Go server runs on Unix")
	}
	serverPath, _ = filepath.Abs(serverPath)
	clientPath, _ = filepath.Abs(clientPath)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	instance := fmt.Sprintf("rust-prototype-%d", os.Getpid())
	home := t.TempDir()
	url := "http://" + address
	logPath := filepath.Join(home, "server.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	env := append(os.Environ(), "AGENTERM_SKIP_LINK_REGISTRATION=1", "HOME="+home, "TMPDIR="+home, "AGENTERM_LANG=en", "P2P_CLI_STUN=none")
	keyFile := filepath.Join(home, "management-key")
	if err := os.WriteFile(keyFile, []byte("prototype-test-password"), 0600); err != nil {
		t.Fatal(err)
	}
	server := exec.Command(serverPath, "start", "-instance", instance, "-listen", address, "-public-url", url, "-passwd-file", keyFile, "-enable-short-code")
	server.Env = env
	server.Stdout = log
	server.Stderr = log
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Wait() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		stop := exec.CommandContext(ctx, serverPath, "stop", "-instance", instance)
		stop.Env = env
		if out, err := stop.CombinedOutput(); err != nil {
			t.Errorf("stop isolated server: %v %s", err, out)
		}
		select {
		case <-serverDone:
		case <-time.After(5 * time.Second):
			server.Process.Kill()
			<-serverDone
			t.Error("server did not stop")
		}
		if t.Failed() {
			b, _ := os.ReadFile(logPath)
			if len(b) > 3000 {
				b = b[len(b)-3000:]
			}
			t.Logf("isolated server log: %s", b)
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		r, err := (&http.Client{Timeout: 200 * time.Millisecond}).Get(url + "/healthz")
		if err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("isolated server did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "rust-prototype-test", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url + "/agenterm/mcp", HTTPClient: &http.Client{Transport: authenticatedTransport{"prototype-test-password"}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("%s failed: %v", name, result.Content)
		}
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	created := call("create_client", map[string]any{})
	control := created["control_token"].(string)
	_, invitation, ok := strings.Cut(created["display_text"].(string), "\n\nBrowser invitation link:\n")
	line, _, _ := strings.Cut(invitation, "\n")
	link := line[strings.LastIndexByte(line, ' ')+1:]
	if !ok || !strings.Contains(link, "/c/") {
		t.Fatal("connection instructions are missing the browser invitation link")
	}
	clientLog, err := os.Create(filepath.Join(home, "client.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer clientLog.Close()
	client := exec.Command(clientPath, "-token", link, "-connection-mode", "persistent")
	remoteState := filepath.Join(home, "p2p-remote")
	client.Env = append(env, "SHELL=/bin/sh", "NO_COLOR=1", "P2P_CLI_STATE_DIR="+remoteState,
		"XDG_CACHE_HOME="+filepath.Join(home, "cache-remote"))
	client.Stdout = clientLog
	client.Stderr = clientLog
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	clientDone := make(chan error, 1)
	go func() { clientDone <- client.Wait() }()
	t.Cleanup(func() {
		client.Process.Signal(os.Interrupt)
		select {
		case err := <-clientDone:
			if err != nil {
				t.Errorf("client exit: %v", err)
			}
		case <-time.After(5 * time.Second):
			client.Process.Kill()
			<-clientDone
			t.Error("client did not stop")
		}
		if t.Failed() {
			b, _ := os.ReadFile(filepath.Join(home, "client.log"))
			t.Logf("Rust client log: %s", b)
		}
	})
	listed := call("list_client", map[string]any{"control_token": control, "wait_ms": 5000})
	clients := listed["clients"].([]any)
	if len(clients) != 1 {
		t.Fatal("Rust client did not connect to the real runtime")
	}
	id := clients[0].(map[string]any)["client_id"]
	result := call("execute_command", map[string]any{"control_token": control, "client_id": id, "command": "export RUST_REAL_SERVER=works; printf 'rust-through-mcp\\n'", "setup": map[string]any{"session_id": "smoke"}})
	if result["status"] != "idle" || !strings.Contains(result["output"].(string), "rust-through-mcp") {
		t.Fatal(result)
	}
	result = call("execute_command", map[string]any{"control_token": control, "client_id": id, "session_id": "smoke", "command": "printf '%s' \"$RUST_REAL_SERVER\""})
	if !strings.Contains(result["output"].(string), "works") {
		t.Fatal(result)
	}
	stopped := call("session_control", map[string]any{"control_token": control, "client_id": id, "session_id": "smoke", "action": "stop"})
	if stopped["status"] != "closed" {
		t.Fatal(stopped)
	}
	agentState := filepath.Join(home, "p2p-agent")
	agentEnv := append(env, "P2P_CLI_STATE_DIR="+agentState, "XDG_CACHE_HOME="+filepath.Join(home, "cache-agent"))
	testRustP2PRoundTrip(t, ctx, agentEnv, agentState, remoteState, func() map[string]any {
		return call("p2p_transfer_start", map[string]any{"control_token": control, "client_id": id})
	}, func() error { return client.Process.Signal(os.Interrupt) })
}
