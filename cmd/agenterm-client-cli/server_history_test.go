package main

import (
	"agenterm/internal/client"
	"bufio"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func historyTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestPlainShortCodeSelectsOneHistoricalServer(t *testing.T) {
	historyTestHome(t)
	if err := client.RememberServer("ws://first.example/custom/client"); err != nil {
		t.Fatal(err)
	}
	if err := client.RememberServer("wss://second.example/agenterm/client"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	got, err := selectPlainServer(bufio.NewReader(strings.NewReader("2\n")), &out, "ABC123")
	if err != nil || got != "http://first.example/custom/c/ABC123" || !strings.Contains(out.String(), "https://second.example/agenterm") {
		t.Fatalf("got=%q output=%q err=%v", got, out.String(), err)
	}
}

func TestTUIShortCodeOnlyContactsSelectedServer(t *testing.T) {
	historyTestHome(t)
	var first, second atomic.Int32
	hit := make(chan struct{}, 1)
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Add(1)
		http.Error(w, "invalid", http.StatusUnauthorized)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		second.Add(1)
		select {
		case hit <- struct{}{}:
		default:
		}
		http.Error(w, "invalid", http.StatusUnauthorized)
	}))
	defer b.Close()
	for _, server := range []string{b.URL, a.URL} {
		if err := client.RememberServer("ws" + strings.TrimPrefix(server, "http") + "/agenterm/client"); err != nil {
			t.Fatal(err)
		}
	}
	m := newTUI("ABC123")
	defer m.cancel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.selectingServer || len(m.serverChoices) != 2 || first.Load() != 0 || second.Load() != 0 {
		t.Fatal("short code did not prompt without sending")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.selectingMode || first.Load() != 0 || second.Load() != 0 {
		t.Fatal("short code must select a mode before contacting the server")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case <-hit:
	case <-time.After(3 * time.Second):
		t.Fatal("selected server was not contacted")
	}
	if first.Load() != 0 || second.Load() != 1 {
		t.Fatalf("first=%d second=%d", first.Load(), second.Load())
	}
}

func TestTUIShortCodeWithoutHistory(t *testing.T) {
	historyTestHome(t)
	m := newTUI("ABC123")
	defer m.cancel()
	m.connect()
	if m.selectingServer || m.state != "disconnected" || m.message != tr("history.server.empty") {
		t.Fatalf("state=%s message=%s", m.state, m.message)
	}
}
