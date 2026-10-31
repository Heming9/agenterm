package main

import (
	"agenterm/internal/client"
	"errors"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestInvitationProgressRequiresConnectedEvent(t *testing.T) {
	m := newTUI("")
	defer m.cancel()
	m.connectionProgress = true
	m.generation = 1
	m.state = "connecting"
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	if !strings.Contains(m.View(), "[3/4]") || strings.Contains(m.View(), progressConnected) {
		t.Fatalf("premature connection success: %s", m.View())
	}
	m.Update(tuiEvent{generation: 1, finished: true, err: errors.New("handshake rejected")})
	if strings.Contains(m.View(), progressConnected) || !strings.Contains(m.View(), "handshake rejected") {
		t.Fatalf("failed handshake progress: %s", m.View())
	}
	m.state = "connecting"
	m.generation = 2
	m.Update(tuiEvent{generation: 1, connected: true})
	if strings.Contains(m.View(), progressConnected) {
		t.Fatal("stale handshake completed progress")
	}
	m.Update(tuiEvent{generation: 2, connected: true})
	if !strings.Contains(m.View(), progressConnected) {
		t.Fatalf("successful handshake missing progress: %s", m.View())
	}
	header := ansi.Strip(strings.Split(m.View(), "\n")[0])
	if !strings.HasSuffix(header, "已连接 ") || strings.Contains(header, "[4/4]") {
		t.Fatalf("connected header still shows progress: %q", header)
	}
}

func TestRedeemedInvitationUsesReusableTokenOnRetry(t *testing.T) {
	m := newTUI("invitation")
	defer m.cancel()
	m.invitation = "invitation"
	m.generation = 1
	m.Update(tuiEvent{generation: 0, redeemed: "stale"})
	if m.invitation != "invitation" || m.input.Value() != "invitation" {
		t.Fatal("stale redemption replaced the current credential")
	}
	m.Update(tuiEvent{generation: 1, redeemed: "reusable-token"})
	if m.invitation != "" || m.input.Value() != "reusable-token" {
		t.Fatal("retry would redeem the consumed invitation again")
	}
}

func TestDisplayDecoderSplitSequences(t *testing.T) {
	d := &displayDecoder{}
	got := d.text("A\x1b[3") + d.text("1m红\x1b]0;bad") + d.text("\aB")
	if got != "A红B" {
		t.Fatalf("got %q", got)
	}
}
func TestTUIEventsAndBoundedLog(t *testing.T) {
	m := newTUI("")
	defer m.cancel()
	m.generation = 1
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	m.Update(tuiEvent{generation: 1, connected: true})
	m.Update(tuiEvent{generation: 1, command: &client.CommandEvent{ID: "one", Command: "echo hi"}})
	m.Update(tuiEvent{generation: 1, id: "one", output: []byte("\x1b[2Jhello")})
	m.Update(tuiEvent{generation: 1, command: &client.CommandEvent{ID: "one", Done: true}})
	if m.completed != 1 || !strings.Contains(m.logs, "hello") || strings.Contains(m.logs, "\x1b") {
		t.Fatalf("invalid log: %q", m.logs)
	}
	m.Update(tuiEvent{generation: 0, connected: true})
	if m.completed != 1 {
		t.Fatal("stale event reset state")
	}
	m.appendLog("output", strings.Repeat("界", maxTUILog))
	if len(m.logs) > maxTUILog || !utf8.ValidString(m.logs) {
		t.Fatal("invalid bounded log")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if m.logs != "" {
		t.Fatal("clear failed")
	}
}

func TestGUILogFormatAndColors(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	m := newTUI("")
	defer m.cancel()
	m.generation = 1
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 15})
	m.Update(tuiEvent{generation: 1, connected: true})
	started := time.Date(2026, 1, 1, 14, 32, 8, 0, time.UTC)
	m.Update(tuiEvent{generation: 1, command: &client.CommandEvent{ID: "first", Command: "echo hello", StartedAt: started}})
	m.Update(tuiEvent{generation: 1, command: &client.CommandEvent{ID: "second", Command: "pwd", StartedAt: started}})
	m.Update(tuiEvent{generation: 1, id: "first", output: []byte("hello\n")})
	m.Update(tuiEvent{generation: 1, command: &client.CommandEvent{ID: "first", Done: true, Error: "failed"}})
	want := "s1 14:32:08 $ echo hello\n\ns2 14:32:08 $ pwd\n\ns1\nhello\nfailed\n"
	if m.logs != want {
		t.Fatalf("log = %q, want %q", m.logs, want)
	}
	view := m.View()
	for _, color := range []string{"38;2;153;177;195", "38;2;133;201;155", "38;2;255;138;138"} {
		if !strings.Contains(view, color) {
			t.Errorf("missing color %s in %q", color, view)
		}
	}
	lipgloss.SetHasDarkBackground(false)
	m.refreshLog()
	view = m.View()
	for _, color := range []string{"38;2;75;103;121", "38;2;36;116;69", "38;2;197;48;48"} {
		if !strings.Contains(view, color) {
			t.Errorf("missing light color %s in %q", color, view)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 18, Height: 20})
	if !strings.Contains(strings.ReplaceAll(ansi.Strip(m.View()), "\n", ""), "echo hello") {
		t.Errorf("wrapped command missing: %q", ansi.Strip(m.View()))
	}
	if strings.Contains(view, "Finished") {
		t.Error("completion footer should not be shown in log")
	}
}

func TestTUIOutputSpacingAndClear(t *testing.T) {
	m := newTUI("")
	defer m.cancel()
	m.Update(tuiEvent{connected: true})
	started := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m.Update(tuiEvent{command: &client.CommandEvent{ID: "one", Command: "pwd", StartedAt: started}})
	for _, chunk := range []string{"\r\n", " \r\nPath\r", "\n----\r\nC:\\Users\\dmhe", "\r\n\r\n", "\r\n"} {
		m.Update(tuiEvent{id: "one", output: []byte(chunk)})
	}
	m.Update(tuiEvent{command: &client.CommandEvent{ID: "two", Command: "printf test", StartedAt: started}})
	m.Update(tuiEvent{id: "two", output: []byte("test")})
	m.Update(tuiEvent{command: &client.CommandEvent{ID: "three", Command: "next", StartedAt: started}})
	want := "s1 12:00:00 $ pwd\nPath\n----\nC:\\Users\\dmhe\n\ns2 12:00:00 $ printf test\ntest\n\ns3 12:00:00 $ next\n"
	if m.logs != want {
		t.Fatalf("log = %q, want %q", m.logs, want)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m.Update(tuiEvent{id: "one", output: []byte("\n  after clear\n\n")})
	if want := "s1\n  after clear\n"; m.logs != want {
		t.Fatalf("output after clear = %q, want %q", m.logs, want)
	}
}

func TestHeaderRightAlignmentAndNarrowWidth(t *testing.T) {
	m := newTUI("")
	defer m.cancel()
	m.state = "connected"
	m.address = "ws://127.0.0.1:18743/agenterm/client"
	for _, width := range []int{80, 40, 25} {
		m.width = width
		header := ansi.Strip(strings.Split(m.View(), "\n")[0])
		if ansi.StringWidth(header) > width {
			t.Errorf("width %d: %q overflows", width, header)
		}
		if !strings.HasSuffix(header, "已连接 ") {
			t.Errorf("width %d: status not right aligned: %q", width, header)
		}
		if width == 80 && !strings.Contains(header, m.address+"  ·  已连接") {
			t.Errorf("address and status order: %q", header)
		}
	}
}

func TestStatusColors(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	m := newTUI("")
	defer m.cancel()
	m.width = 80
	for _, tc := range []struct{ state, label, color string }{
		{"connected", "已连接", "38;2;133;201;155"},
		{"connecting", "正在连接", "38;2;230;190;105"},
		{"disconnected", "已断开", "38;2;163;163;163"},
	} {
		m.state = tc.state
		header := strings.Split(m.View(), "\n")[0]
		if !strings.Contains(header, tc.color) || !strings.Contains(header, tc.label) {
			t.Errorf("%s: %q", tc.state, header)
		}
	}
}
