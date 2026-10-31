package client

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func isolatedLockHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func requireDuplicateConnection(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(i18n.Localize(err, "en"), "already has a connection") {
		t.Fatalf("expected duplicate connection error, got %v", err)
	}
}

// Run in another process to exercise actual kernel locks and crash cleanup.
func TestConnectionLockProcess(t *testing.T) {
	address := os.Getenv("AGENTERM_TEST_LOCK_SERVER")
	if address == "" {
		return
	}
	lock, err := acquireConnectionLock(address)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestConnectionLockAcrossProcesses(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(fmt.Sprintf("crash=%v", crash), func(t *testing.T) {
			isolatedLockHome(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			address := "wss://example.test/agenterm/client"
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConnectionLockProcess$")
			cmd.Env = append(os.Environ(), "AGENTERM_TEST_LOCK_SERVER="+address)
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "locked\n" {
				t.Fatalf("child did not acquire lock: %q %v", line, err)
			}
			// Equivalent host spelling and the implicit TLS port share a lock.
			_, err = acquireConnectionLock("wss://EXAMPLE.TEST:443/agenterm/client")
			requireDuplicateConnection(t, err)
			other, err := acquireConnectionLock("wss://example.test:8443/agenterm/client")
			if err != nil {
				t.Fatalf("different server blocked: %v", err)
			}
			other.Close()
			if crash {
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			} else {
				input.Close()
			}
			if err := cmd.Wait(); err != nil && !crash {
				t.Fatal(err)
			}
			lock, err := acquireConnectionLock(address)
			if err != nil {
				t.Fatalf("lock not released after process exit: %v", err)
			}
			lock.Close()
		})
	}
}

func TestConcurrentConnectionReservations(t *testing.T) {
	isolatedLockHome(t)
	const attempts = 8
	results := make(chan *os.File, attempts)
	for range attempts {
		go func() {
			file, _ := acquireConnectionLock("ws://example.test/agenterm/client")
			results <- file
		}()
	}
	winners := 0
	for range attempts {
		if file := <-results; file != nil {
			winners++
			defer file.Close()
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent attempts acquired %d locks", winners)
	}
}

func TestDuplicateInvitationsDoNotRedeem(t *testing.T) {
	isolatedLockHome(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	address := "ws" + strings.TrimPrefix(server.URL, "http") + protocol.ClientPath
	lock, err := acquireConnectionLock(address)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	for _, run := range []func(context.Context, string, io.Writer, Callbacks) error{RunWithCallbacks, RunInvitationWithCallbacks} {
		for _, secret := range []byte{1, 2} {
			credential, err := protocol.Encode(protocol.Token{URL: address, Secret: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{secret}, protocol.TokenSecretSize))})
			if err != nil {
				t.Fatal(err)
			}
			err = run(context.Background(), credential, io.Discard, Callbacks{
				Connected: func() { t.Error("duplicate connected") },
				Redeemed:  func(string) { t.Error("duplicate invitation redeemed") },
			})
			requireDuplicateConnection(t, err)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("duplicate attempts made %d server requests", requests.Load())
	}
}

func TestInvitationConnectionHoldsLockThroughRedemptionAndConnection(t *testing.T) {
	isolatedLockHome(t)
	var address, token, invitation string
	var redemptions atomic.Int32
	assertReserved := func() {
		file, err := acquireConnectionLock(address)
		if file != nil {
			file.Close()
		}
		if err == nil || !strings.Contains(i18n.Localize(err, "en"), "already has a connection") {
			t.Errorf("connection reservation missing: %v", err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertReserved()
		w.Header().Set(version.Header, version.Current)
		if r.URL.Path == protocol.PathPrefix+"/invite/redeem" {
			redemptions.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
			return
		}
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, http.Header{version.Header: {version.Current}})
		if err == nil {
			defer ws.Close()
			_, _, _ = ws.ReadMessage()
		}
	}))
	defer server.Close()
	address = "ws" + strings.TrimPrefix(server.URL, "http") + protocol.ClientPath
	var err error
	token, err = protocol.Encode(protocol.Token{URL: address, Secret: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, protocol.TokenSecretSize))})
	if err != nil {
		t.Fatal(err)
	}
	invitation, err = protocol.Encode(protocol.Token{URL: address, Secret: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, protocol.TokenSecretSize))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	redeemed, connected := false, false
	err = RunInvitationWithCallbacks(ctx, invitation, io.Discard, Callbacks{
		Redeemed: func(value string) {
			if value != token {
				t.Errorf("wrong reusable token: %q", value)
			}
			redeemed = true
			assertReserved()
		},
		Connected: func() {
			connected = true
			if !redeemed {
				t.Error("connected before receiving the reusable token")
			}
			assertReserved()
			err := RunInvitationWithCallbacks(ctx, invitation, io.Discard, Callbacks{})
			requireDuplicateConnection(t, err)
			cancel()
		},
	})
	if err != nil || !connected || redemptions.Load() != 1 {
		t.Fatalf("connected=%v, redemptions=%d: %v", connected, redemptions.Load(), err)
	}
	lock, err := acquireConnectionLock(address)
	if err != nil {
		t.Fatalf("reservation leaked after invitation connection: %v", err)
	}
	lock.Close()
}

func TestConnectionLockReleasedAfterDisconnectOrFailure(t *testing.T) {
	isolatedLockHome(t)
	var reject atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reject.Load() || r.URL.Path != protocol.ClientPath {
			http.Error(w, "rejected", http.StatusForbidden)
			return
		}
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, http.Header{version.Header: {version.Current}})
		if err == nil {
			defer ws.Close()
			_, _, _ = ws.ReadMessage()
		}
	}))
	defer server.Close()
	address := "ws" + strings.TrimPrefix(server.URL, "http") + protocol.ClientPath
	credential, err := protocol.Encode(protocol.Token{URL: address, Secret: base64.RawURLEncoding.EncodeToString(make([]byte, protocol.TokenSecretSize))})
	if err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{false, true, false} {
		reject.Store(fail)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := Run(ctx, credential, io.Discard, cancel)
		cancel()
		if (err != nil) != fail {
			t.Fatalf("reject=%v: %v", fail, err)
		}
		lock, err := acquireConnectionLock(address)
		if err != nil {
			t.Fatalf("reservation leaked: %v", err)
		}
		lock.Close()
	}
	if err := RunInvitationWithCallbacks(context.Background(), credential, io.Discard, Callbacks{}); err == nil {
		t.Fatal("expected redemption failure")
	}
	lock, err := acquireConnectionLock(address)
	if err != nil {
		t.Fatalf("reservation leaked after redemption failure: %v", err)
	}
	lock.Close()
}
