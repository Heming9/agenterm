package service

import (
	terminal "agenterm/internal/client"

	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFullClientRejectsAnonymousAndMissingNamedTerminals(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	connectExecutionClient(t, h, token)
	initialize, overwrite, inspect := "keep=initial", "keep=unexpected", "printf '%s' \"$keep\""
	if runtime.GOOS == "windows" {
		initialize, overwrite, inspect = "$keep='initial'", "$keep='unexpected'", "Write-Output $keep"
	}
	first := h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": "original"}, "command": initialize}, false).(map[string]any)
	for range 9 {
		h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "command": "echo ready"}, false)
	}
	for _, args := range []map[string]any{
		{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "command": "echo must-not-run"},
		{"wait_ms": 0, "command": overwrite},
	} {
		denied := h.call(t, token, "execute_command", args, true).(map[string]any)
		if !strings.Contains(denied["error"].(string), "10") {
			t.Fatal(denied)
		}
	}
	reused := h.call(t, token, "execute_command", map[string]any{"session_id": first["session_id"], "command": inspect}, false).(map[string]any)
	if reused["status"] != "idle" || strings.TrimSpace(reused["output"].(string)) != "initial" {
		t.Fatalf("rejected request altered terminal: %v", reused)
	}
	h.call(t, token, "session_control", map[string]any{"session_id": first["session_id"], "action": "stop"}, false)
	closed := h.call(t, token, "session_read", map[string]any{"session_id": first["session_id"]}, false).(map[string]any)
	if closed["status"] != "closed" {
		t.Fatal(closed)
	}
	h.call(t, token, "execute_command", map[string]any{"setup": map[string]any{"session_id": random()}, "wait_ms": 0, "command": "echo replacement"}, false)
}

func TestConcurrentSessionStartsRespectLimit(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared_name=%t", shared), func(t *testing.T) {
			h := newMultiHarness(t, 0)
			token := h.create(t, false)
			connectExecutionClient(t, h, token)
			var workers sync.WaitGroup
			results := make(chan bool, 12)
			for range 12 {
				workers.Go(func() {
					name, command := random(), "echo accepted"
					if shared {
						name, command = "shared", longCommand()
					}
					_, failed, err := h.request(token, "execute_command", map[string]any{"setup": map[string]any{"session_id": name}, "wait_ms": 0, "command": command})
					if err != nil {
						t.Error(err)
					}
					results <- err == nil && !failed
				})
			}
			workers.Wait()
			close(results)
			accepted, want := 0, 10
			if shared {
				want = 1
			}
			for ok := range results {
				if ok {
					accepted++
				}
			}
			items := h.call(t, token, "session_list", map[string]any{}, false).(map[string]any)["sessions"].([]any)
			if accepted != want || len(items) != want || (shared && items[0].(map[string]any)["session_id"] != "shared") {
				t.Fatalf("accepted=%d want=%d sessions=%v", accepted, want, items)
			}
		})
	}
}

func TestSameSessionNameIsIndependentAcrossDevices(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, true)
	var devices []string
	for _, name := range []string{"first", "second"} {
		ws, id := h.connect(t, token, name)
		devices = append(devices, id)
		go serveTestDevice(ws, name, nil, nil)
	}
	var wg sync.WaitGroup
	for _, id := range devices {
		wg.Go(func() {
			out, failed, err := h.request(token, "execute_command", map[string]any{"client_id": id, "setup": map[string]any{"session_id": "same"}, "command": "once"})
			if err != nil || failed || out.(map[string]any)["session_id"] != "same" {
				t.Errorf("%v %v %v", out, failed, err)
			}
		})
	}
	wg.Wait()
	list := h.call(t, token, "session_list", nil, false).(map[string]any)["sessions"].([]any)
	if len(list) != 2 {
		t.Fatal(list)
	}
	h.call(t, token, "session_read", map[string]any{"session_id": "same"}, true)
	for _, id := range devices {
		h.call(t, token, "session_read", map[string]any{"session_id": "same", "client_id": id}, false)
	}
}
func TestZeroWaitReturnsClientAdmissionResult(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, false)
	ws, _ := h.connect(t, token, "admission")
	received, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	go serveAPI(ws, testAPI{execute: func(ctx context.Context, r terminal.ExecuteRequest) (terminal.Output, error) {
		if r.WaitMS == nil || *r.WaitMS != 0 {
			t.Error("wait option lost")
		}
		close(received)
		select {
		case <-release:
		case <-ctx.Done():
			return terminal.Output{}, ctx.Err()
		}
		return terminal.Output{SessionID: "client-id", CommandID: "c", Status: "running"}, nil
	}})
	finished := make(chan error, 1)
	go func() {
		_, failed, err := h.request(token, "execute_command", map[string]any{"command": "waiting", "wait_ms": 0})
		if failed && err == nil {
			err = fmt.Errorf("rejected")
		}
		finished <- err
	}()
	<-received
	select {
	case err := <-finished:
		t.Fatalf("server invented admission: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	release <- struct{}{}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
