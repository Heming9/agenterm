package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type connectionPollControl struct {
	control
	waits   chan int
	failure error
}

func (c *connectionPollControl) call(ctx context.Context, method string, in, out any) error {
	if method == "clients" {
		c.waits <- in.(toolRequest[listClientsArgs]).Args.WaitMS
		*out.(*[]clientInfo) = []clientInfo{}
		return c.failure
	}
	return c.control.call(ctx, method, in, out)
}

func TestMCPConnectionPollBoundsLegacyWaitWithRetainedRuntime(t *testing.T) {
	state := &runtimeState{adminKey: "admin-test", clientURL: "ws://example.com/agenterm/client"}
	c := &connectionPollControl{control: localControl{state}, waits: make(chan int, 1)}
	front := &frontend{control: c, clientURL: state.clientURL}
	ts := httptest.NewServer(front.routes(installerTestTag, nil))
	defer ts.Close()
	h := &multiHarness{s: state, ts: ts}
	token := h.create(t, false)
	for requested, want := range map[int]int{0: 0, 20: 20, 30000: 30000, 300000: 30000} {
		out := h.call(t, token, "list_client", map[string]any{"wait_ms": requested}, false).(map[string]any)
		if got := <-c.waits; got != want {
			t.Fatalf("requested %d: forwarded %d want %d", requested, got, want)
		}
		if len(out) != 1 || len(out["clients"].([]any)) != 0 {
			t.Fatal("poll changed the snapshot contract", out)
		}
	}
	for _, invalid := range []int{-1, 300001} {
		h.call(t, token, "list_client", map[string]any{"wait_ms": invalid}, true)
	}
}

func TestMCPConnectionPollFailureIsNotAnEmptySnapshot(t *testing.T) {
	state := &runtimeState{adminKey: "admin-test", clientURL: "ws://example.com/agenterm/client"}
	c := &connectionPollControl{control: localControl{state}, waits: make(chan int, 1), failure: errors.New("runtime unavailable")}
	front := &frontend{control: c, clientURL: state.clientURL}
	ts := httptest.NewServer(front.routes(installerTestTag, nil))
	defer ts.Close()
	h := &multiHarness{s: state, ts: ts}
	token := h.create(t, false)
	out := h.call(t, token, "list_client", map[string]any{"wait_ms": 0}, true).(map[string]any)
	message, _ := out["error"].(string)
	if out["clients"] != nil || !strings.Contains(message, "runtime unavailable") {
		t.Fatal("failed check was misrepresented as offline", out)
	}
}

func TestMCPConnectionPollReturnsBeforeProxyTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("exercises a real 30-second no-device poll")
	}
	state := &runtimeState{adminKey: "admin-test", clientURL: "ws://example.com/agenterm/client"}
	front := state.testFrontend()
	// Simulate a proxy with less patience than the legacy 300-second request.
	ts := httptest.NewServer(http.TimeoutHandler(front.routes(installerTestTag, nil), 35*time.Second, "proxy timeout"))
	defer ts.Close()
	h := &multiHarness{s: state, ts: ts}
	token := h.create(t, false)
	out, failed, err := h.request(token, "list_client", map[string]any{"wait_ms": 300000}, 40*time.Second)
	if err != nil || failed {
		t.Fatalf("legacy poll failed through proxy: failed=%v err=%v", failed, err)
	}
	if len(out.(map[string]any)["clients"].([]any)) != 0 {
		t.Fatal("unexpected online device", out)
	}
}
