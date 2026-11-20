package interop

import (
	"agenterm/internal/protocol"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func waitForP2PFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("bootstrap did not write %s", filepath.Base(path))
	return nil
}

func waitForP2PStateRemoval(t *testing.T, dirs ...string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for _, dir := range dirs {
		for {
			_, err := os.Stat(filepath.Join(dir, "daemon.json"))
			if os.IsNotExist(err) {
				break
			}
			if err != nil || time.Now().After(deadline) {
				t.Fatalf("P2P helper did not clean up in %s: %v", dir, err)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
}

func TestRustCLIP2PBootstrapLifecycle(t *testing.T) {
	for _, action := range []string{"complete", "stop", "reconnect"} {
		t.Run(action, func(t *testing.T) {
			f := newFixture(t, false, "persistent")
			dir := t.TempDir()
			config := base64.StdEncoding.EncodeToString([]byte("private negotiation test config"))
			started := filepath.Join(dir, "start.txt")
			stopped := filepath.Join(dir, "stop.txt")
			sh := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			ps := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
			script := "printf '%s' \"$AGENTERM_P2P_CONFIG_B64\" > " + sh(started) + "\n"
			stopScript := "printf stopped > " + sh(stopped)
			if runtime.GOOS == "windows" {
				script = "[IO.File]::WriteAllText(" + ps(started) + ", $env:AGENTERM_P2P_CONFIG_B64)\n"
				stopScript = "[IO.File]::WriteAllText(" + ps(stopped) + ", 'stopped')"
			}
			switch {
			case action != "complete" && runtime.GOOS == "windows":
				script += "Start-Sleep -Seconds 60\n"
			case action != "complete":
				script += "sleep 60\n"
			case runtime.GOOS != "windows":
				// A descendant can still hold the output pipe after the
				// wrapper exits. The completion must not wait for its EOF.
				script += "sleep 60 &\nexit 0\n"
			}
			start := protocol.Message{Type: "p2p_start", ID: "test-p2p", P2P: &protocol.P2PLaunch{
				Script: script, StopScript: stopScript, ConfigBase64: config,
			}}
			if err := f.peer.WriteJSON(start); err != nil {
				t.Fatal(err)
			}
			if string(waitForP2PFile(t, started)) != config {
				t.Fatal("bootstrap did not receive the negotiation parameters in its environment")
			}
			switch action {
			case "stop":
				if err := f.peer.WriteJSON(protocol.Message{Type: "p2p_stop", ID: start.ID}); err != nil {
					t.Fatal(err)
				}
			case "reconnect":
				f.peer.Close()
				f.nextPeer()
				if f.resumes.Load() != 1 {
					t.Fatal("P2P cleanup prevented the client from resuming")
				}
			}
			if action != "reconnect" {
				f.peer.SetReadDeadline(time.Now().Add(5 * time.Second))
				messageType, data, err := f.peer.ReadMessage()
				if err != nil {
					t.Fatal(err)
				}
				var reply map[string]any
				if err := json.Unmarshal(data, &reply); err != nil || messageType != websocket.TextMessage ||
					len(reply) != 2 || reply["type"] != "p2p_done" || reply["id"] != start.ID {
					t.Fatal("bootstrap did not return a credential-free completion acknowledgment")
				}
			}
			waitForP2PFile(t, stopped)
			if result := f.call("list", map[string]any{}); len(result["sessions"].([]any)) != 0 {
				t.Fatal("P2P consumed a managed terminal")
			}
		})
	}
}

func testRustP2PRoundTrip(
	t *testing.T,
	ctx context.Context,
	env []string,
	agentState, remoteState string,
	start func() map[string]any,
	disconnect func() error,
) {
	t.Helper()
	state := start()
	command := exec.CommandContext(ctx, "/bin/sh", "-c", state["shell_script"].(string))
	command.Env = env
	var result struct{ Executable string }
	output, err := command.Output()
	if err != nil || json.Unmarshal(output, &result) != nil || result.Executable == "" {
		t.Fatalf("could not start local P2P component: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, result.Executable, "stop")
		cmd.Env = env
		_ = cmd.Run()
	})
	local := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, result.Executable, args...)
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("local p2p-cli %s: %v: %s", args[0], err, output)
		}
		return output
	}
	for deadline := time.Now().Add(8 * time.Second); ; {
		var status struct{ State string }
		if err := json.Unmarshal(local("status"), &status); err == nil && status.State == "connected" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("local P2P component did not connect")
		}
		time.Sleep(25 * time.Millisecond)
	}
	localID, remoteID := state["local_client_id"].(string), state["remote_client_id"].(string)
	dir := t.TempDir()
	source, remote, recalled := filepath.Join(dir, "源文件.txt"), filepath.Join(dir, "remote.txt"), filepath.Join(dir, "recalled.txt")
	payload := bytes.Repeat([]byte("agenterm p2p\x00\xff\n"), 4096)
	if err := os.WriteFile(source, payload, 0600); err != nil {
		t.Fatal(err)
	}
	local("cp", localID+":"+source, remoteID+":"+remote)
	local("cp", remoteID+":"+remote, localID+":"+recalled)
	received, err := os.ReadFile(recalled)
	if err != nil || !bytes.Equal(received, payload) {
		t.Fatal("P2P round trip changed file contents")
	}
	local("stop")
	waitForP2PStateRemoval(t, agentState, remoteState)
	// Ask the same connected device to bootstrap again. This requires the
	// previous foreground process to acknowledge completion to the runtime.
	for deadline := time.Now().Add(5 * time.Second); ; {
		if next := start(); next["connection_id"] != state["connection_id"] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime reused the completed P2P connection")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := disconnect(); err != nil {
		t.Fatal(err)
	}
	waitForP2PStateRemoval(t, remoteState)
}
