package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agenterm/internal/protocol"
	"agenterm/internal/version"
)

func TestUnusedTokenCleanupWithAndWithoutQuota(t *testing.T) {
	for _, quota := range []int{0, 2} {
		for _, redeemed := range []bool{false, true} {
			s := &server{clientURL: "wss://example.com/agenterm/client", maxClientsPerIP: quota}
			issued, err := s.createClient("192.0.2.1")
			if err != nil {
				t.Fatal(err)
			}
			token := issued["client_token"].(string)
			decoded, _ := protocol.Decode(token)
			invitation, _ := protocol.Decode(issued["invitation"].(string))
			if redeemed {
				req := httptest.NewRequest(http.MethodPost, "/invite/redeem", nil)
				req.Header.Set("Authorization", "Bearer "+invitation.Secret)
				req.Header.Set(version.Header, version.Current)
				response := httptest.NewRecorder()
				s.redeemInvitation(response, req)
				if response.Code != http.StatusOK {
					t.Fatalf("redeem: %d", response.Code)
				}
			}
			// Cleanup must remove associated output, not just authentication data.
			s.sessions = map[string]*session{"history": {owner: decoded.Secret, done: true, ended: time.Now()}}
			s.cleanupTokensLocked(s.clientOwners[decoded.Secret].expires)
			if len(s.tokens)+len(s.clientOwners)+len(s.multiDevice)+len(s.invitations)+len(s.installCodes)+len(s.sessions) != 0 {
				t.Fatalf("quota=%d redeemed=%v: expired token or associated state retained", quota, redeemed)
			}
		}
	}
}

func TestMultiDeviceTokenOfflineExpiryAndReconnect(t *testing.T) {
	h := newMultiHarness(t, 0)
	token := h.create(t, true)
	decoded, _ := protocol.Decode(token)
	first, _ := h.connect(t, token, "first")
	second, _ := h.connect(t, token, "second")
	go serveTestDevice(first, "first", nil, nil)
	go serveTestDevice(second, "second", nil, nil)
	waitForClients := func(want int) time.Time {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			h.s.mu.Lock()
			count := len(h.s.clients)
			expires := h.s.clientOwners[decoded.Secret].expires
			h.s.mu.Unlock()
			if count == want {
				return expires
			}
			if time.Now().After(deadline) {
				t.Fatalf("online clients=%d, want %d", count, want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	first.Close()
	if expires := waitForClients(1); !expires.IsZero() {
		t.Fatal("expiry started while another device was still online")
	}
	h.s.mu.Lock()
	// Even after invitation expiration, online tokens and their mode survive.
	h.s.cleanupTokensLocked(time.Now().Add(48 * time.Hour))
	_, retained := h.s.tokens[token]
	multi := h.s.multiDevice[decoded.Secret]
	h.s.mu.Unlock()
	if !retained || !multi {
		t.Fatal("online multi-device token reclaimed")
	}
	second.Close()
	expires := waitForClients(0)
	if remaining := time.Until(expires); remaining < tokenOfflineTTL-time.Minute || remaining > tokenOfflineTTL {
		t.Fatalf("unexpected offline expiry: %s", remaining)
	}
	h.s.mu.Lock()
	h.s.cleanupTokensLocked(expires.Add(-time.Nanosecond))
	_, retained = h.s.tokens[token]
	h.s.mu.Unlock()
	if !retained {
		t.Fatal("offline token reclaimed before its deadline")
	}
	reconnected, _ := h.connect(t, token, "reconnected")
	go serveTestDevice(reconnected, "reconnected", nil, nil)
	if expires := waitForClients(1); !expires.IsZero() {
		t.Fatal("reconnect did not clear offline expiry")
	}
	reconnected.Close()
	expires = waitForClients(0)
	h.s.mu.Lock()
	h.s.cleanupTokensLocked(expires)
	_, retained = h.s.tokens[token]
	h.s.mu.Unlock()
	if retained {
		t.Fatal("offline token retained after its deadline")
	}
	if conn, response, err := h.dial(token, "expired"); err == nil {
		conn.Close()
		t.Fatal("expired token reconnected")
	} else if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized after expiry: response=%v err=%v", response, err)
	} else {
		response.Body.Close()
	}
}

func TestTokenCleanupRunsWithoutNewRequests(t *testing.T) {
	s := &server{clientURL: "wss://example.com/agenterm/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := protocol.Decode(issued["client_token"].(string))
	s.clientOwners[decoded.Secret].expires = time.Now().Add(-time.Second)
	// The single startup credential remains reusable for console invitations.
	s.tokens["startup"] = "startup-secret"
	s.sessions = map[string]*session{"old": {done: true, ended: time.Now().Add(-time.Hour)}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.cleanupLoop(ctx, time.Millisecond)
	}()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		_, expiredRetained := s.tokens[issued["client_token"].(string)]
		clean := !expiredRetained && len(s.tokens) == 1 && s.tokens["startup"] == "startup-secret" &&
			len(s.clientOwners)+len(s.invitations)+len(s.installCodes)+len(s.multiDevice)+len(s.sessions) == 0
		s.mu.Unlock()
		if clean {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("periodic cleanup did not reclaim expired state")
		}
		time.Sleep(time.Millisecond)
	}
}
