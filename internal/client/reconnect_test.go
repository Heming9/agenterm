package client

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func connectionToken(t *testing.T, address string, secret byte) string {
	t.Helper()
	b := make([]byte, protocol.TokenSecretSize)
	b[0] = secret
	token, err := protocol.Encode(protocol.Token{URL: "ws" + strings.TrimPrefix(address, "http") + protocol.ClientPath, Secret: base64.RawURLEncoding.EncodeToString(b)})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestReconnectReusesRedeemedTokenAndDoesNotReplayCommands(t *testing.T) {
	isolatedLockHome(t)
	var token string
	var redemptions, connections, commands, completions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agenterm/invite/redeem" {
			redemptions.Add(1)
			w.Header().Set(version.Header, version.Current)
			json.NewEncoder(w).Encode(map[string]string{"token": token})
			return
		}
		decoded, _ := protocol.Decode(token)
		if r.Header.Get("Authorization") != "Bearer "+decoded.Secret {
			t.Error("reconnected with invitation instead of token")
		}
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, http.Header{version.Header: {version.Current}})
		if err != nil {
			return
		}
		defer ws.Close()
		n := connections.Add(1)
		if n == 1 {
			ws.WriteJSON(protocol.Message{Type: "exec", ID: "once", Command: "echo once"})
			for {
				var m protocol.Message
				if ws.ReadJSON(&m) != nil {
					return
				}
				if m.Type == "done" {
					return
				} // Abrupt EOF forces a reconnect.
			}
		}
		var m protocol.Message
		ws.ReadJSON(&m)
	}))
	defer server.Close()
	token = connectionToken(t, server.URL, 1)
	invitation := connectionToken(t, server.URL, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var connected, retries int
	err := RunInvitationWithCallbacks(ctx, invitation, io.Discard, Callbacks{
		Connected: func() {
			connected++
			if connected == 2 {
				if commands.Load() != 1 || completions.Load() != 1 {
					t.Error("old command callbacks not completed before reconnect")
				}
				cancel()
			}
		},
		Command: func(e CommandEvent) {
			if e.Done {
				completions.Add(1)
			} else {
				commands.Add(1)
			}
		},
		Reconnecting: func(e ReconnectEvent) {
			retries++
			if e.Attempt != 1 || e.Delay != time.Second || e.Err == nil {
				t.Errorf("bad retry event: %+v", e)
			}
			decoded, _ := protocol.Decode(token)
			lock, err := acquireConnectionLock(decoded.URL)
			if lock != nil {
				lock.Close()
			}
			if err == nil {
				t.Error("lock released during backoff")
			}
		},
	})
	if err != nil || connected != 2 || retries != 1 || redemptions.Load() != 1 || commands.Load() != 1 {
		t.Fatalf("err=%v connected=%d retries=%d redemptions=%d commands=%d", err, connected, retries, redemptions.Load(), commands.Load())
	}
}

func TestReconnectHTTPFailuresAndCancellation(t *testing.T) {
	for _, status := range []int{401, 403, 404, 426, 408, 409, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Retry-After", "300")
				w.WriteHeader(status)
			}))
			defer s.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var retried bool
			err := RunWithCallbacks(ctx, connectionToken(t, s.URL, 1), io.Discard, Callbacks{Reconnecting: func(e ReconnectEvent) {
				retried = true
				if e.Delay != 300*time.Second {
					t.Errorf("ignored Retry-After: %s", e.Delay)
				}
				cancel()
			}})
			transient := status == 408 || status == 409 || status == 429 || status >= 500
			if retried != transient || (err == nil) != transient || requests.Load() != 1 {
				t.Fatalf("retry=%v err=%v requests=%d", retried, err, requests.Load())
			}
		})
	}
}

func TestHeartbeatResponseAndLatencyCallback(t *testing.T) {
	var pong atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, http.Header{version.Header: {version.Current}, protocol.HeartbeatHeader: {"1"}})
		if err != nil {
			return
		}
		defer ws.Close()
		ws.SetPongHandler(func(s string) error {
			if s != "sample" {
				t.Errorf("unexpected pong %q", s)
			}
			pong.Store(true)
			return ws.WriteJSON(protocol.Message{Type: "latency", LatencyMS: 42})
		})
		ws.WriteControl(websocket.PingMessage, []byte("sample"), time.Now().Add(time.Second))
		ws.SetReadDeadline(time.Now().Add(2 * time.Second))
		var m protocol.Message
		ws.ReadJSON(&m)
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var latency time.Duration
	err := RunWithCallbacks(ctx, connectionToken(t, s.URL, 1), io.Discard, Callbacks{Latency: func(d time.Duration) { latency = d; cancel() }})
	if err != nil || !pong.Load() || latency != 42*time.Millisecond {
		t.Fatalf("err=%v pong=%v latency=%s", err, pong.Load(), latency)
	}
}

func TestRetryBackoffAndRetryAfter(t *testing.T) {
	delay := time.Second
	for _, want := range []time.Duration{1, 2, 4, 8, 16, 30, 30, 30} {
		if delay != want*time.Second {
			t.Fatalf("delay=%s want=%ds", delay, want)
		}
		delay = nextRetryDelay(delay)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for value, want := range map[string]time.Duration{"300": 300 * time.Second, "bad": 0, "-1": 0, now.Add(45 * time.Second).Format(http.TimeFormat): 45 * time.Second} {
		if got := retryAfter(value, now); got != want {
			t.Errorf("%q: %s != %s", value, got, want)
		}
	}
}
