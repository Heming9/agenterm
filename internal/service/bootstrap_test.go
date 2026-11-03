//go:build !windows

package service

import (
	remoteclient "agenterm/internal/client"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"agenterm/internal/invite"
	"agenterm/internal/p2p"
	"crypto/sha256"
	"encoding/hex"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerAutomaticallyStartsAndReusesRuntime(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ag-auto-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMPDIR", dir)
	// A standalone Server must use its embedded components even when an old
	// deployment still supplies the former external P2P directory setting.
	t.Setenv("P2P_CLI_ASSETS_DIR", filepath.Join(dir, "no-external-components"))
	paths, err := pathsForInstance(defaultInstance)
	if err != nil {
		t.Fatal(err)
	}
	socket := paths.Runtime
	c := newRuntimeClient(socket)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = controlCall[bool](ctx, c, "runtime/shutdown", struct{}{})
		c.http.CloseIdleConnections()
	})
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	reserved.Close()
	origin := "http://" + address
	args := []string{"-instance", defaultInstance, "-listen", address, "-public-url", origin, "-passwd", "admin-test", "-max-clients-per-ip", "2"}
	executable := filepath.Join(dir, "agenterm-server")
	build := exec.Command("go", "build", "-o", executable, "./cmd/agenterm-server")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	logs, err := os.CreateTemp(dir, "server-*.log")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	first := &serviceProcess{cmd: exec.Command(executable, args...), done: make(chan error, 1), log: logs.Name()}
	// A terminal interrupt reaches the entire server process group. The runtime
	// must live in another session, and must not retain the server's output pipe.
	first.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	first.cmd.Stdout, first.cmd.Stderr = &stdout, io.MultiWriter(logs, &stderr)
	if err := first.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { first.done <- first.cmd.Wait(); logs.Close() }()
	t.Cleanup(func() { _ = first.cmd.Process.Kill() })
	waitReady(t, origin+"/healthz", http.DefaultTransport, first)
	componentDirs, err := filepath.Glob(filepath.Join(paths.Dir, "components", "*", "p2p-cli"))
	if err != nil || len(componentDirs) != 1 {
		t.Fatalf("server did not extract its embedded components: %v %v", componentDirs, err)
	}
	peerManifest, err := p2p.Load(componentDirs[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range peerManifest.Artifacts {
		response, err := http.Get(origin + "/agenterm/p2p/downloads/" + artifact.File)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		hash := sha256.Sum256(data)
		if err != nil || response.StatusCode != 200 || hex.EncodeToString(hash[:]) != artifact.SHA256 {
			t.Fatalf("embedded P2P download differs: %s HTTP %d %v", artifact.File, response.StatusCode, err)
		}
	}
	config, err := controlCall[runtimeConfig](context.Background(), c, "config", struct{}{})
	if err != nil || config.PID == first.cmd.Process.Pid {
		t.Fatalf("independent runtime: %+v %v", config, err)
	}
	h := &multiHarness{ts: &httptest.Server{URL: origin}}
	token := h.create(t, false)
	clientCtx, stopClient := context.WithCancel(context.Background())
	clientDone := make(chan error, 1)
	var connections atomic.Int32
	go func() {
		clientDone <- remoteclient.RunWithCallbacks(clientCtx, token, io.Discard, remoteclient.Callbacks{Connected: func() { connections.Add(1) }})
	}()
	t.Cleanup(func() {
		stopClient()
		select {
		case <-clientDone:
		case <-time.After(5 * time.Second):
			t.Error("client did not exit")
		}
	})
	devices := h.call(t, token, "list_clients", map[string]any{"wait_ms": 4000}, false).(map[string]any)["clients"].([]any)
	if len(devices) != 1 {
		t.Fatal(devices)
	}
	deviceID := devices[0].(map[string]any)["client_id"]
	id := h.call(t, token, "session_start", map[string]any{"command": `printf 'ready\n'; read answer; printf 'answer:%s\n' "$answer"`}, false).(map[string]any)["session_id"].(string)
	before := h.call(t, token, "session_read", map[string]any{"session_id": id, "wait_ms": 4000}, false).(map[string]any)
	if !strings.Contains(before["output"].(string), "ready") {
		t.Fatal(before)
	}
	if err := syscall.Kill(-first.cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-first.done:
		if err != nil {
			t.Fatalf("server interrupted: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runtime retained parent output pipe or prevented server exit")
	}
	if !strings.Contains(stdout.String(), origin+"/agenterm/mcp") {
		t.Fatalf("runtime startup output was lost: %q", stdout.String())
	}
	stillRunning, err := controlCall[runtimeConfig](context.Background(), c, "config", struct{}{})
	if err != nil || stillRunning.PID != config.PID {
		t.Fatalf("runtime died with parent: %+v %v", stillRunning, err)
	}
	replacement := startService(t, "server", dir, args...)
	waitReady(t, origin+"/healthz", http.DefaultTransport, replacement)
	after, err := controlCall[runtimeConfig](context.Background(), c, "config", struct{}{})
	if err != nil || after.PID != config.PID {
		t.Fatalf("replacement started another runtime: %+v %v", after, err)
	}
	h.call(t, token, "session_control", map[string]any{"action": "input", "session_id": id, "input": "hello\n"}, false)
	var output strings.Builder
	cursor := before["next_cursor"]
	for {
		read := h.call(t, token, "session_read", map[string]any{"session_id": id, "cursor": cursor, "wait_ms": 4000}, false).(map[string]any)
		output.WriteString(read["output"].(string))
		cursor = read["next_cursor"]
		if read["done"] == true && read["has_more"] == false {
			break
		}
	}
	if !strings.Contains(output.String(), "answer:hello") {
		t.Fatalf("session interrupted: %q", output.String())
	}
	devices = h.call(t, token, "list_clients", map[string]any{}, false).(map[string]any)["clients"].([]any)
	if connections.Load() != 1 || devices[0].(map[string]any)["client_id"] != deviceID {
		t.Fatal("client reconnected during automatic bootstrap")
	}
	changed := append([]string(nil), args...)
	changed[len(changed)-1] = "3"
	if output, err := exec.Command(executable, changed...).CombinedOutput(); err == nil || !strings.Contains(string(output), "different -max-clients-per-ip") {
		t.Fatalf("changed runtime flags were silently ignored: %v\n%s", err, output)
	}
	if output, err := exec.Command(executable, append(args, "-passwd", "different-secret")...).CombinedOutput(); err == nil || !strings.Contains(string(output), "different -passwd") || strings.Contains(string(output), "different-secret") {
		t.Fatalf("credential mismatch: %v\n%s", err, output)
	}
	// There is no public runtime subcommand or independently runnable worker.
	if output, err := exec.Command(executable, "runtime").CombinedOutput(); err == nil || !strings.Contains(string(output), "unexpected argument") {
		t.Fatalf("standalone runtime entrypoint: %v\n%s", err, output)
	}
	invalidWorker := exec.Command(executable)
	invalidWorker.Env = append(os.Environ(), "AGENTERM_INTERNAL_RUNTIME=1")
	if output, err := invalidWorker.CombinedOutput(); err == nil || !strings.Contains(string(output), "must be started by") {
		t.Fatalf("unguarded worker entrypoint: %v\n%s", err, output)
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	failedArgs := []string{"-instance", "failed", "-listen", occupied.Addr().String()}
	failedOutput, failedErr := exec.Command(executable, failedArgs...).CombinedOutput()
	occupied.Close()
	if failedErr == nil || !strings.Contains(string(failedOutput), "address already in use") {
		t.Fatalf("runtime startup error was hidden: %v\n%s", failedErr, failedOutput)
	}
	// Named instances must create a distinct runtime, and stopping one must
	// not disturb the default instance or its existing device connection.
	isolatedPort, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	isolatedAddress := isolatedPort.Addr().String()
	isolatedPort.Close()
	stagedPaths, err := pathsForInstance("staging")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(executable, "stop", "-instance", "staging").Run() })
	staging := startService(t, "server", dir, "-instance", "staging", "-listen", isolatedAddress, "-public-url", "http://"+isolatedAddress, "-passwd", "staging-key")
	waitProcessBackend(t, stagedPaths, staging)
	staged := newRuntimeClient(stagedPaths.Runtime)
	stagedConfig, err := controlCall[runtimeConfig](context.Background(), staged, "config", struct{}{})
	staged.http.CloseIdleConnections()
	if err != nil || stagedConfig.PID == config.PID {
		t.Fatalf("instances shared a runtime: %+v %v", stagedConfig, err)
	}
	if output, err := exec.Command(executable, "status", "-instance", "staging").CombinedOutput(); err != nil || !strings.Contains(string(output), "staging") {
		t.Fatalf("named status: %v\n%s", err, output)
	}
	if output, err := exec.Command(executable, "stop", "-instance", "staging").CombinedOutput(); err != nil {
		t.Fatalf("named stop: %v\n%s", err, output)
	}
	staging.stop(t, false)
	if output, err := exec.Command(executable, "status").CombinedOutput(); err != nil {
		t.Fatalf("stopping staging affected default: %v\n%s", err, output)
	}
	replacement.stop(t, false)
	if output, err := exec.Command(executable, "status").CombinedOutput(); err == nil || !strings.Contains(string(output), "runtime running") {
		t.Fatalf("runtime-only status: %v\n%s", err, output)
	}
	// Deliberately stop the detached runtime through the same server binary.
	if output, err := exec.Command(executable, "stop").CombinedOutput(); err != nil {
		t.Fatalf("explicit runtime stop: %v\n%s", err, output)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := controlCall[runtimeConfig](context.Background(), c, "config", struct{}{}); runtimeAbsent(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("runtime did not stop")
}

func TestRuntimeSettingsCheckOnlyExplicitFlags(t *testing.T) {
	s := &runtimeState{adminKey: "secret", startupSettings: &runtimeSettings{Listen: "127.0.0.1:9090", PublicURL: "https://example.com/custom", Prefix: "/custom", ServerSocket: "/tmp/server.sock", MaxClientsPerIP: 2, ClientIPHeader: "X-Real-Ip"}}
	defaults := addRuntimeLaunchFlags(flag.NewFlagSet("bootstrap", flag.ContinueOnError))
	defaults.Fields = map[string]bool{}
	if _, err := s.checkRuntimeSettings(context.Background(), *defaults); err != nil {
		t.Fatal(err)
	}
	for _, name := range runtimeSettingFlags {
		o := *defaults
		o.Fields = map[string]bool{name: true}
		if name == "no-password" {
			o.Settings.NoPassword = true
		}
		if _, err := s.checkRuntimeSettings(context.Background(), o); err == nil {
			t.Fatalf("ignored explicit mismatch: %s", name)
		}
	}
	matching := runtimeLaunchOptions{Settings: *s.startupSettings, Password: "secret", Fields: map[string]bool{}}
	for _, name := range runtimeSettingFlags {
		matching.Fields[name] = true
	}
	if _, err := s.checkRuntimeSettings(context.Background(), matching); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRuntimeStdioStartupAndEOF(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ag-stdio-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	executable := filepath.Join(dir, "agenterm-server")
	build := exec.Command("go", "build", "-o", executable, "./cmd/agenterm-server")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	reserved.Close()
	t.Setenv("TMPDIR", dir)
	logs, err := os.CreateTemp(dir, "stdio-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	cmd := exec.Command(executable, "-stdio", "-listen", address, "-public-url", "http://"+address)
	cmd.Stderr = logs
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	defer stdin.Close()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, executable, "stop").Run()
	})
	ended := make(chan error, 1)
	go func() { ended <- cmd.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "managed-stdio", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: stdout, Writer: stdin}, nil)
	if err != nil {
		t.Fatalf("startup output contaminated stdio: %v", err)
	}
	defer session.Close()
	created, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "create_client", Arguments: map[string]any{}})
	if err != nil || created.IsError {
		t.Fatalf("create: %v %v", created, err)
	}
	var credential struct {
		Invitation  string `json:"invitation"`
		ClientToken string `json:"client_token"`
	}
	data, err := json.Marshal(created.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &credential); err != nil {
		t.Fatal(err)
	}
	_ = session.Close()
	_ = stdin.Close()
	select {
	case err := <-ended:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stdio EOF did not exit server")
	}
	if token, err := invite.Redeem(ctx, credential.Invitation); err != nil || token != credential.ClientToken {
		t.Fatalf("runtime did not survive stdio EOF: %v", err)
	}
	startup, err := os.ReadFile(logs.Name())
	if err != nil {
		t.Fatal(err)
	}
	prefix := strings.Split(serverText("mcp.management.key.save.securely.s"), "%s")[0]
	if !bytes.Contains(startup, []byte(prefix)) {
		t.Fatalf("generated management key was not returned on stderr: %s", startup)
	}
}
