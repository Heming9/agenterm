package service

import (
	terminal "agenterm/internal/client"
	"agenterm/internal/protocol"
	"context"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCloseClientSelectionAndIsolation(t *testing.T) {
	h := newMultiHarness(t, 0)
	token, other := h.create(t, true), h.create(t, true)
	first, firstID := h.connect(t, token, "first")
	_, secondID := h.connect(t, token, "second")
	_, otherID := h.connect(t, other, "other")
	for _, ids := range [][]string{{firstID, otherID}, {firstID, "zzzzzz"}, {firstID, firstID}, {}} {
		h.call(t, token, "close_client", map[string]any{"client_ids": ids}, true)
	}
	if got := h.call(t, token, "list_client", map[string]any{}, false).(map[string]any)["clients"].([]any); len(got) != 2 {
		t.Fatalf("validation closed devices: %v", got)
	}
	out := h.call(t, "admin-test", "close_client", map[string]any{"client_token": token, "client_ids": []string{firstID}}, false).(map[string]any)
	if ids := out["client_ids"].([]any); len(ids) != 1 || ids[0] != firstID {
		t.Fatal(out)
	}
	_, _, err := first.ReadMessage()
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("expected permanent close: %v", err)
	}
	remaining := h.call(t, token, "list_client", map[string]any{}, false).(map[string]any)["clients"].([]any)
	if len(remaining) != 1 || remaining[0].(map[string]any)["client_id"] != secondID {
		t.Fatal(remaining)
	}
	out = h.call(t, token, "close_client", map[string]any{}, false).(map[string]any)
	if ids := out["client_ids"].([]any); len(ids) != 1 || ids[0] != secondID {
		t.Fatal(out)
	}
	h.call(t, "admin-test", "list_client", map[string]any{"client_token": token}, true)
	h.call(t, "admin-test", "close_client", map[string]any{"client_token": token}, true)
	if _, resp, err := h.dial(token, "reconnect"); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("revoked token reconnected: response=%v error=%v", resp, err)
	}
	decoded, err := protocol.Decode(token)
	if err != nil {
		t.Fatal(err)
	}
	h.s.mu.Lock()
	for _, invitation := range h.s.invitations {
		if invitation.token == token {
			t.Error("invitation survived token revocation")
		}
	}
	if h.s.validSecretLocked(decoded.Secret) {
		t.Error("token survived revocation")
	}
	h.s.mu.Unlock()
	if got := h.call(t, other, "list_client", map[string]any{}, false).(map[string]any)["clients"].([]any); len(got) != 1 {
		t.Fatal(got)
	}
}

func TestCloseClientRevokesTokenWithoutOnlineDevices(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, true)
	out := h.call(t, "admin-test", "close_client", map[string]any{"client_token": token}, false).(map[string]any)
	if len(out["client_ids"].([]any)) != 0 {
		t.Fatal(out)
	}
	h.call(t, "admin-test", "list_client", map[string]any{"client_token": token}, true)
}

func TestCloseClientRevocationWakesConnectionWaiter(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, true)
	decoded, err := protocol.Decode(token)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := h.s.waitForClients(context.Background(), decoded.Secret, time.Minute)
		result <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		h.s.mu.Lock()
		registered := h.s.clientWaiters[decoded.Secret] != nil
		h.s.mu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiter did not register")
		}
		time.Sleep(time.Millisecond)
	}
	h.call(t, "admin-test", "close_client", map[string]any{"client_token": token}, false)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("revoked token did not fail the waiter")
		}
	case <-time.After(time.Second):
		t.Fatal("revocation did not wake the waiter")
	}
}

func TestCloseClientStopsQueuedCommand(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, true)
	_, id := h.connect(t, token, "queued")
	decoded, err := protocol.Decode(token)
	if err != nil {
		t.Fatal(err)
	}
	h.s.mu.Lock()
	c := h.s.clients[id]
	h.s.mu.Unlock()
	c.write.Lock()
	result := make(chan error, 1)
	go func() {
		_, err := h.s.executeCommand(context.Background(), toolRequest[executeArgs]{Secret: decoded.Secret, Args: executeArgs{ClientID: id, ExecuteRequest: terminal.ExecuteRequest{Command: "must not run"}}})
		result <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		c.mu.Lock()
		registered := len(c.pending) != 0
		c.mu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			c.write.Unlock()
			t.Fatal("command did not reach the send queue")
		}
		time.Sleep(time.Millisecond)
	}
	h.call(t, token, "close_client", map[string]any{}, false)
	c.write.Unlock()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("queued command was sent after closure")
		}
	case <-time.After(time.Second):
		t.Fatal("queued command did not finish")
	}
}
