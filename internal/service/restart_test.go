//go:build !windows

package service

import (
	remoteclient "agenterm/internal/client"
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"context"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Exercise the actual release entrypoints in independent processes. Build
// ordinary binaries even under -race; unit tests separately cover core races.
type serviceProcess struct {
	cmd  *exec.Cmd
	done chan error
	log  string
}

func startService(t *testing.T, role, dir string, args ...string) *serviceProcess {
	t.Helper()
	executable := filepath.Join(dir, "agenterm-"+role)
	if _, err := os.Stat(executable); os.IsNotExist(err) {
		build := exec.Command("go", "build", "-o", executable, "./cmd/agenterm-"+role)
		build.Dir = "../.."
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", role, err, output)
		}
	}
	logfile, err := os.CreateTemp(dir, role+"-*.log")
	if err != nil {
		t.Fatal(err)
	}
	p := &serviceProcess{cmd: exec.Command(executable, args...), done: make(chan error, 1), log: logfile.Name()}
	p.cmd.Stdout, p.cmd.Stderr = logfile, logfile
	if err := p.cmd.Start(); err != nil {
		logfile.Close()
		t.Fatal(err)
	}
	go func() { p.done <- p.cmd.Wait(); logfile.Close() }()
	t.Cleanup(func() { _ = p.cmd.Process.Kill() })
	return p
}

func (p *serviceProcess) stop(t *testing.T, hard bool) {
	t.Helper()
	if hard {
		_ = p.cmd.Process.Kill()
	} else {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case err := <-p.done:
		if err != nil && !hard {
			logs, _ := os.ReadFile(p.log)
			t.Fatalf("service shutdown: %v\n%s", err, logs)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("service did not exit")
	}
}

func waitReady(t *testing.T, address string, transport http.RoundTripper, p *serviceProcess) {
	t.Helper()
	c := &http.Client{Transport: transport, Timeout: time.Second}
	defer c.CloseIdleConnections()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		response, err := c.Get(address)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case err := <-p.done:
			logs, _ := os.ReadFile(p.log)
			t.Fatalf("service exited before ready: %v\n%s", err, logs)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	logs, _ := os.ReadFile(p.log)
	t.Fatalf("service not ready: %s\n%s", address, logs)
}

func TestServerUpgradeAndCrashPreserveClientAndSession(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ag-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	reserved.Close()
	origin := "http://" + address
	t.Setenv("TMPDIR", dir)
	paths, err := pathsForInstance(defaultInstance)
	if err != nil {
		t.Fatal(err)
	}
	runtimeSocket := paths.Runtime
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client := newRuntimeClient(runtimeSocket)
		defer client.http.CloseIdleConnections()
		_, _ = controlCall[bool](ctx, client, "runtime/shutdown", struct{}{})
	})
	initialArgs := []string{"-listen", address, "-public-url", origin, "-passwd", "admin-test"}
	a := startService(t, "server", dir, initialArgs...)
	waitReady(t, origin+"/healthz", http.DefaultTransport, a)
	h := &multiHarness{ts: &httptest.Server{URL: origin}}
	token := h.create(t, false)
	// Leave another invitation unredeemed across both kinds of server restart.
	pending := h.call(t, "admin-test", "create_client", map[string]any{}, false).(map[string]any)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan remoteclient.CommandEvent, 32)
	var connected atomic.Int32
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- remoteclient.RunWithCallbacks(ctx, token, io.Discard, remoteclient.Callbacks{
			Connected: func() { connected.Add(1) },
			Command:   func(e remoteclient.CommandEvent) { events <- e },
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-clientDone:
		case <-time.After(5 * time.Second):
			t.Error("client did not exit")
		}
	})
	devices := h.call(t, token, "list_clients", map[string]any{"wait_ms": 4000}, false).(map[string]any)["clients"].([]any)
	if len(devices) != 1 {
		t.Fatalf("devices: %v", devices)
	}
	deviceID := devices[0].(map[string]any)["client_id"]
	started := h.call(t, token, "execute_command", map[string]any{"terminal_name": random(), "wait_ms": 0, "command": `printf 'before\n'; read line; printf 'after:%s\n' "$line"`}, false).(map[string]any)
	id := started["session_id"].(string)
	before := h.call(t, token, "session_read", map[string]any{"session_id": id, "wait_ms": 4000}, false).(map[string]any)
	if !strings.Contains(before["output"].(string), "before") {
		t.Fatalf("session output: %v", before)
	}
	cursor := before["next_cursor"]
	// Start a synchronous request on A, then switch new traffic to ready B.
	release := filepath.Join(dir, "release-command")
	command := `while [ ! -f "$AGENTERM_RELEASE_FILE" ]; do sleep 0.02; done; printf 'drained\n'`
	requestDone := make(chan error, 1)
	go func() {
		result, isError, err := h.request(token, "execute_command", map[string]any{"terminal_name": random(), "wait_ms": 20000, "command": command, "env": map[string]string{"AGENTERM_RELEASE_FILE": release}}, 90*time.Second)
		if err == nil && (isError || !strings.Contains(result.(map[string]any)["output"].(string), "drained")) {
			err = fmt.Errorf("in-flight result: %v", result)
		}
		requestDone <- err
	}()
	waitCommandEvent(t, events, command, false)
	b := startService(t, "server", dir)
	waitProcessBackend(t, paths, b)
	select {
	case err := <-a.done:
		t.Fatalf("old server exited before its request drained: %v", err)
	default:
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-requestDone; err != nil {
		t.Fatalf("in-flight MCP did not drain: %v", err)
	}
	a.stop(t, false)
	// Abruptly kill B while another synchronous command is executing.
	crashCommand := `sleep 1; printf 'survived crash\n'`
	crashWaiter := make(chan error, 1)
	go func() {
		_, _, err := h.request(token, "execute_command", map[string]any{"terminal_name": random(), "wait_ms": 20000, "command": crashCommand})
		crashWaiter <- err
	}()
	waitCommandEvent(t, events, crashCommand, false)
	b.stop(t, true)
	if err := <-crashWaiter; err == nil {
		t.Fatal("killed server unexpectedly returned a command result")
	}
	c := startService(t, "server", dir)
	waitReady(t, origin+"/healthz", http.DefaultTransport, c)
	finished := waitCommandEvent(t, events, crashCommand, true)
	if finished.ExitCode != 0 || finished.Error != "" {
		t.Fatalf("command interrupted by server crash: %+v", finished)
	}
	retained := h.call(t, token, "session_list", map[string]any{}, false).(map[string]any)["sessions"].([]any)
	if len(retained) != 3 || retained[0].(map[string]any)["session_id"] != id {
		t.Fatalf("could not rediscover session after server replacement: %v", retained)
	}
	// Resume the original terminal using the same session ID and byte cursor.
	h.call(t, token, "session_control", map[string]any{"action": "input", "session_id": id, "input": "hello\n"}, false)
	var output strings.Builder
	for {
		read := h.call(t, token, "session_read", map[string]any{"session_id": id, "cursor": cursor, "wait_ms": 4000}, false).(map[string]any)
		output.WriteString(read["output"].(string))
		cursor = read["next_cursor"]
		if read["command_done"] == true {
			if read["exit_code"] != float64(0) {
				t.Fatal(read)
			}
			break
		}
	}
	if !strings.Contains(output.String(), "after:hello") {
		t.Fatalf("output after restart: %q", output.String())
	}
	after := h.call(t, token, "list_clients", map[string]any{}, false).(map[string]any)["clients"].([]any)
	if len(after) != 1 || after[0].(map[string]any)["client_id"] != deviceID || connected.Load() != 1 {
		t.Fatalf("client reconnected across server restart: %v count=%d", after, connected.Load())
	}
	if redeemed, err := invite.Redeem(ctx, pending["invitation"].(string)); err != nil || redeemed != pending["client_token"] {
		t.Fatalf("invitation lost across upgrade: %v", err)
	}
	if info, err := os.Stat(runtimeSocket); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("control socket permissions: %v %v", info, err)
	}
	c.stop(t, false)
	// Even with no business server, the runtime accepts input/output and keeps
	// the device socket alive. The public MCP endpoint reports unavailability.
	response, err := http.Get(origin + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || connected.Load() != 1 {
		t.Fatalf("server downtime: %d", response.StatusCode)
	}
	restartArgs := append([]string{"restart"}, initialArgs...)
	fresh := startService(t, "server", dir, restartArgs...)
	waitReady(t, origin+"/healthz", http.DefaultTransport, fresh)
	decoded, _ := protocol.Decode(token)
	ws, response, err := websocket.DefaultDialer.Dial(decoded.ConnectionURL(), http.Header{"Authorization": {"Bearer " + decoded.Secret}})
	if ws != nil {
		ws.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("runtime restart restored in-memory credentials: %v %v", response, err)
	}
	response.Body.Close()

}

func waitCommandEvent(t *testing.T, events <-chan remoteclient.CommandEvent, command string, done bool) remoteclient.CommandEvent {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		select {
		case e := <-events:
			if e.Command == command && e.Done == done {
				return e
			}
		case <-timer.C:
			t.Fatalf("missing command lifecycle event: done=%v command=%s", done, command)
		}
	}
}

func TestSocketOwnershipAndLiveListenerProtection(t *testing.T) {
	dir, err := os.MkdirTemp("", "ag-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "service.sock")
	listener, err := listenUnix(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if duplicate, err := listenUnix(socket); err == nil {
		duplicate.Close()
		t.Fatal("replaced a live listener")
	}
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatalf("original listener damaged: %v", err)
	}
	conn.Close()
	if err := os.WriteFile(filepath.Join(dir, "ordinary"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenUnix(filepath.Join(dir, "ordinary")); err == nil {
		t.Fatal("removed a regular file")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := listenUnix(filepath.Join(dir, "unsafe.sock")); err == nil {
		t.Fatal("accepted a public socket directory")
	}
}

func TestIngressOverwritesPublicPeerClaims(t *testing.T) {
	dir, err := os.MkdirTemp("", "ag-peer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "server.sock")
	listener, err := listenUnix(socket)
	if err != nil {
		t.Fatal(err)
	}
	peer := make(chan string, 1)
	forwardedIP := make(chan string, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer <- r.Header.Get(remoteAddrHeader)
		forwardedIP <- r.Header.Get("X-Forwarded-For")
		w.WriteHeader(http.StatusNoContent)
	})}
	go server.Serve(listener)
	defer server.Close()
	state := &runtimeState{clientURL: "ws://example.com/agenterm/client", clientIPHeader: "X-Forwarded-For"}
	ingress := httptest.NewServer(state.ingress(socket))
	defer ingress.Close()
	r, _ := http.NewRequest(http.MethodGet, ingress.URL+"/agenterm/mcp", nil)
	r.Header.Set(remoteAddrHeader, "192.0.2.99:1234")
	r.Header.Set("X-Forwarded-For", "192.0.2.10")
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal(response.StatusCode)
	}
	claimed := <-peer
	host, _, err := net.SplitHostPort(claimed)
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("forwarded forged identity: %q", claimed)
	}
	if ip := <-forwardedIP; ip != "192.0.2.10" {
		t.Fatalf("lost configured proxy identity: %q", ip)
	}
}

func waitProcessBackend(t *testing.T, paths instancePaths, process *serviceProcess) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	prefix := "api-" + strconv.Itoa(process.cmd.Process.Pid) + "-"
	for time.Now().Before(deadline) {
		backend, _, err := paths.currentBackend()
		if err == nil && strings.HasPrefix(filepath.Base(backend), prefix) {
			waitReady(t, "http://server/healthz", unixTransport(backend), process)
			return
		}
		select {
		case err := <-process.done:
			logs, _ := os.ReadFile(process.log)
			t.Fatalf("replacement failed: %v\n%s", err, logs)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("replacement did not become the active backend")
}
