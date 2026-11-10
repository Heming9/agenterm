package service

import (
	terminal "agenterm/internal/client"
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSingleDeviceResumeTakeover(t *testing.T) {
	h := newMultiHarness(t, 1)
	issued := h.call(t, "admin-test", "create_client", map[string]any{}, false).(map[string]any)
	token := issued["client_token"].(string)
	decoded, err := protocol.Decode(token)
	if err != nil {
		t.Fatal(err)
	}
	dial := func(resume string) (*websocket.Conn, *http.Response, error) {
		headers := http.Header{"Authorization": {"Bearer " + decoded.Secret}, version.Header: {version.Current}, protocol.ClientAPIHeader: {terminal.APIVersion}}
		if resume != "" {
			headers.Set(protocol.ResumeCredentialHeader, resume)
		}
		return websocket.DefaultDialer.Dial(decoded.ConnectionURL(), headers)
	}
	first, response, err := dial("")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	resume := response.Header.Get(protocol.ResumeCredentialHeader)
	if len(resume) < 32 {
		t.Fatal("missing resume credential")
	}
	if _, err := invite.Redeem(context.Background(), issued["invitation"].(string)); err == nil {
		t.Fatal("online invitation redeemed")
	}
	for _, key := range []string{"", "wrong"} {
		conn, response, err := dial(key)
		if conn != nil {
			conn.Close()
		}
		if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("invalid resume accepted: %v", err)
		}
		response.Body.Close()
	}
	second, response, err := dial(resume)
	if err != nil {
		t.Fatalf("resume rejected: %v", err)
	}
	defer second.Close()
	if response.Header.Get(protocol.ResumeCredentialHeader) != resume {
		t.Fatal("resume credential changed during takeover")
	}
	first.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := first.ReadMessage(); err == nil {
		t.Fatal("old connection remained open")
	}
	h.s.mu.Lock()
	count := len(h.s.clients)
	h.s.mu.Unlock()
	if count != 1 {
		t.Fatalf("got %d active connections", count)
	}
	second.Close()
	deadline := time.Now().Add(3 * time.Second)
	for len(h.s.listClients(decoded.Secret)) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("disconnect not observed")
		}
		time.Sleep(time.Millisecond)
	}
	if redeemed, err := invite.Redeem(context.Background(), issued["invitation"].(string)); err != nil || redeemed != token {
		t.Fatalf("invitation should remain usable after disconnect: %v", err)
	}
	third, response, err := dial("")
	if err != nil {
		t.Fatalf("new device rejected after disconnect: %v", err)
	}
	defer third.Close()
	if response.Header.Get(protocol.ResumeCredentialHeader) == resume {
		t.Fatal("new device reused prior resume credential")
	}
	conn, response, err := dial(resume)
	if conn != nil {
		conn.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old credential took over new device: %v", err)
	}
	response.Body.Close()
}
