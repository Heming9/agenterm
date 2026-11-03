package service

import (
	remoteclient "agenterm/internal/client"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Supply the native assets made by scripts/build_p2p_cli.py. This
// exercises actual downloads, the builtin scripts, Client and real DataChannels.
func TestP2PBootstrapEndToEnd(t *testing.T) {
	for _, format := range []string{"xz", "gzip", "raw"} {
		t.Run(format, func(t *testing.T) {
			if runtime.GOOS == "windows" && format != "xz" {
				t.Skip("PowerShell fallback is exercised by the invitation installer test")
			}
			if format != "xz" {
				bin := t.TempDir()
				installTarShim(t, bin)
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("AGENTERM_TEST_TAR_MODE", format)
			}
			runP2PBootstrapEndToEnd(t)
		})
	}
}
func runP2PBootstrapEndToEnd(t *testing.T) {
	assets := os.Getenv("P2P_CLI_TEST_ASSETS")
	if assets == "" {
		t.Skip("set P2P_CLI_TEST_ASSETS for real component integration")
	}
	assets, _ = filepath.Abs(assets)
	root := t.TempDir()
	remoteHome := filepath.Join(root, "remote-home")
	agentHome := filepath.Join(root, "agent-home")
	remoteState := filepath.Join(root, "remote-state")
	agentState := filepath.Join(root, "agent-state")
	for _, path := range []string{remoteHome, agentHome} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("P2P_CLI_ASSETS_DIR", assets)
	t.Setenv("P2P_CLI_STUN", "none")
	t.Setenv("HOME", remoteHome)
	t.Setenv("USERPROFILE", remoteHome)
	t.Setenv("LOCALAPPDATA", remoteHome)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(remoteHome, "cache"))
	t.Setenv("P2P_CLI_STATE_DIR", remoteState)
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := make(chan error, 1)
	events := make(chan remoteclient.CommandEvent, 16)
	var clientOutput lockedLogBuffer
	go func() {
		ended <- remoteclient.RunWithCallbacks(ctx, token, &clientOutput, remoteclient.Callbacks{Command: func(event remoteclient.CommandEvent) { events <- event }})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.s.listClients(h.s.tokens[token])) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("client did not connect")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-ended:
		case <-time.After(8 * time.Second):
			t.Error("Client did not clean up P2P process")
		}
	})
	// Runtime allows 25 seconds for the remote registration; keep the HTTP
	// deadline longer so slow native runners return the actual bootstrap error.
	data, failed, err := h.request(token, "p2p_transfer_start", map[string]any{}, 35*time.Second)
	bootstrapDeadline := time.Now().Add(time.Minute)
	for err == nil && failed && time.Now().Before(bootstrapDeadline) {
		result, ok := data.(map[string]any)
		message, _ := result["error"].(string)
		if !ok || !strings.Contains(message, "remote P2P component still initializing") {
			break
		}
		data, failed, err = h.request(token, "p2p_transfer_start", map[string]any{}, 35*time.Second)
	}
	if err != nil || failed {
		t.Fatalf("bootstrap: failed=%v err=%v result=%v\nClient output:\n%s", failed, err, data, clientOutput.String())
	}
	result := data.(map[string]any)
	agentEnv := append(os.Environ(), "HOME="+agentHome, "USERPROFILE="+agentHome, "LOCALAPPDATA="+agentHome, "XDG_CACHE_HOME="+filepath.Join(agentHome, "cache"), "P2P_CLI_STATE_DIR="+agentState)
	bootstrapCtx, cancelBootstrap := context.WithTimeout(context.Background(), time.Minute)
	defer cancelBootstrap()
	var bootstrap *exec.Cmd
	if runtime.GOOS == "windows" {
		bootstrap = exec.CommandContext(bootstrapCtx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", result["powershell_script"].(string))
	} else {
		bootstrap = exec.CommandContext(bootstrapCtx, "/bin/sh", "-c", result["shell_script"].(string))
	}
	bootstrap.WaitDelay = 2 * time.Second
	bootstrap.Env = agentEnv
	output, err := bootstrap.CombinedOutput()
	if err != nil {
		t.Fatalf("Agent bootstrap: %v %s", err, output)
	}
	var started map[string]any
	if err = json.Unmarshal(output, &started); err != nil {
		t.Fatalf("bootstrap output: %s %v", output, err)
	}
	executable, ok := started["executable"].(string)
	if !ok {
		t.Fatal("bootstrap must return its executable path")
	}
	t.Cleanup(func() { command := exec.Command(executable, "stop"); command.Env = agentEnv; _ = command.Run() })
	cli := func(args ...string) map[string]any {
		t.Helper()
		command := exec.Command(executable, args...)
		command.Env = agentEnv
		stdout, err := command.Output()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				t.Fatalf("%v: %s", args, exit.Stderr)
			}
			t.Fatal(err)
		}
		var response map[string]any
		if err = json.Unmarshal(stdout, &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	deadline = time.Now().Add(15 * time.Second)
	for cli("status")["state"] != "connected" {
		if time.Now().After(deadline) {
			t.Fatal("P2P did not connect")
		}
		time.Sleep(25 * time.Millisecond)
	}
	source, destination, returned := filepath.Join(root, "源文件.txt"), filepath.Join(root, "remote-copy.txt"), filepath.Join(root, "return-copy.txt")
	payload := make([]byte, 1024*1024+1)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	if err = os.WriteFile(source, payload, 0600); err != nil {
		t.Fatal(err)
	}
	a, b := result["local_client_id"].(string), result["remote_client_id"].(string)
	upload := cli("cp", a+":"+source, b+":"+destination)
	download := cli("cp", b+":"+destination, a+":"+returned)
	hash := sha256.Sum256(payload)
	expected := hex.EncodeToString(hash[:])
	if upload["sha256"] != expected || download["sha256"] != expected {
		t.Fatal("copy receipts disagree")
	}
	actual, err := os.ReadFile(returned)
	if err != nil || sha256.Sum256(actual) != hash {
		t.Fatal("round-trip file differs")
	}
	cancel()
	deadline = time.Now().Add(8 * time.Second)
	for {
		_, remoteErr := os.Stat(filepath.Join(remoteState, "daemon.json"))
		_, localErr := os.Stat(filepath.Join(agentState, "daemon.json"))
		if os.IsNotExist(remoteErr) && os.IsNotExist(localErr) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Client exit left a P2P process running")
		}
		time.Sleep(25 * time.Millisecond)
	}
	select {
	case event := <-events:
		if event.Command != "p2p-cli start" {
			t.Fatal("bootstrap implementation/credential leaked into displayed command")
		}
	default:
		t.Fatal("missing P2P lifecycle event")
	}
}
