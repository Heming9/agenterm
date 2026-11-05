package main

import (
	"agenterm/internal/client"
	"agenterm/internal/logview"
	"context"
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

func TestLatencyAndReconnectPreserveHistory(t *testing.T) {
	m := newTUI("")
	defer m.cancel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 15})
	m.Update(tuiEvent{connected: true})
	d := 42 * time.Millisecond
	m.Update(tuiEvent{latency: &d})
	header := ansi.Strip(strings.Split(m.View(), "\n")[0])
	if !strings.HasSuffix(header, "已连接 ") || strings.Contains(header, "42ms") || !strings.HasSuffix(ansi.Strip(m.connectedFooter()), "0 条 · 42ms") {
		t.Fatalf("header=%q footer=%q", header, ansi.Strip(m.connectedFooter()))
	}
	m.appendLog("output", "saved output\n")
	m.Update(tuiEvent{retry: &client.ReconnectEvent{Attempt: 1, Delay: time.Second, Err: errors.New("unexpected EOF")}})
	header = ansi.Strip(strings.Split(m.View(), "\n")[0])
	if strings.Contains(m.connectedFooter(), "42ms") || !strings.Contains(header, "重连中") {
		t.Fatal(header)
	}
	m.Update(tuiEvent{latency: &d}) // Ignore stale measurements while offline.
	if m.latency >= 0 {
		t.Fatal("offline latency restored")
	}
	m.Update(tuiEvent{connecting: true})
	m.Update(tuiEvent{connected: true})
	if m.latency >= 0 || !strings.Contains(m.logText(), "saved output") || !strings.Contains(m.logText(), "unexpected EOF") || !strings.Contains(m.logText(), "连接已恢复") {
		t.Fatal(m.logText())
	}
	m.Update(tuiEvent{latency: &d})
	m.Update(tuiEvent{finished: true})
	if m.latency >= 0 {
		t.Fatal("disconnected latency not cleared")
	}
}

func TestDisplayDecoderSplitSequences(t *testing.T) {
	d := &logview.Decoder{}
	got := d.Text("A\x1b[3") + d.Text("1m红\x1b]0;bad") + d.Text("\aB")
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
	if m.completed != 1 || !strings.Contains(m.logText(), "hello") || strings.Contains(m.logText(), "\x1b") {
		t.Fatalf("invalid log: %q", m.logText())
	}
	m.Update(tuiEvent{generation: 0, connected: true})
	if m.completed != 1 {
		t.Fatal("stale event reset state")
	}
	m.appendLog("output", strings.Repeat("界", maxTUILog))
	if len(m.logText()) > maxTUILog || !utf8.ValidString(m.logText()) {
		t.Fatal("invalid bounded log")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if m.logText() == "" {
		t.Fatal("c should not clear history")
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
	want := "14:32:08 $ echo hello\n\n14:32:08 $ pwd\n\nhello\nfailed\n"
	if m.logText() != want {
		t.Fatalf("log = %q, want %q", m.logText(), want)
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
	m.viewport.GotoTop()
	m.Update(tea.MouseMsg{X: 0, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m.Update(tea.MouseMsg{X: 0, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
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
	want := "12:00:00 $ pwd\nPath\n----\nC:\\Users\\dmhe\n\n12:00:00 $ printf test\ntest\n\n12:00:00 $ next\n"
	if m.logText() != want {
		t.Fatalf("log = %q, want %q", m.logText(), want)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m.Update(tuiEvent{id: "one", output: []byte("\n  after clear\n\n")})
	if want := want + "\n  after clear\n"; m.logText() != want {
		t.Fatalf("output after clear = %q, want %q", m.logText(), want)
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

// Plain text is only needed for assertions; the TUI renders its spans directly.
func (m *tuiModel) logText() string {
	var text strings.Builder
	for _, span := range m.spans {
		text.WriteString(span.text)
	}
	return text.String()
}

func TestHeaderRetainsTokenIdentifierWhenAddressIsTruncated(t *testing.T) {
	m := newTUI("")
	defer m.cancel()
	m.width = 56
	m.state = "connected"
	m.address = "wss://very-long-example-server-name.example/client/123abc"
	header := ansi.Strip(m.header("connected"))
	if !strings.Contains(header, "123abc") || !strings.Contains(header, "connected") {
		t.Fatalf("identifier or status missing: %q", header)
	}
}

func TestTUIValidatesExplicitEmptyToken(t *testing.T) {
	manual := newTUI("")
	defer manual.cancel()
	manual.Init()
	if manual.message != "" {
		t.Fatalf("unprovided token should wait for input: %q", manual.message)
	}
	automatic := newTUI("")
	defer automatic.cancel()
	automatic.autoConnect = true
	automatic.Init()
	if automatic.message == "" {
		t.Fatal("explicit empty token should be validated at startup")
	}
}

func TestCtrlCClearsInputBeforeQuitting(t *testing.T) {
	m := newTUI("expired invitation")
	defer m.cancel()
	m.invitation = m.input.Value()
	m.message = "invalid token"
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil || m.ctx.Err() != nil || m.input.Value() != "" || m.message != "" || m.invitation != "" {
		t.Fatalf("first Ctrl+C should clear input: input=%q message=%q invitation=%q err=%v", m.input.Value(), m.message, m.invitation, m.ctx.Err())
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || m.ctx.Err() == nil {
		t.Fatal("Ctrl+C with an empty input should quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected quit command")
	}
}

func TestConnectedFooterAndDisconnect(t *testing.T) {
	m := newTUI("saved-token")
	defer m.cancel()
	m.state = "connected"
	m.completed = 12
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	line := ansi.Strip(m.connectedFooter())
	if !strings.HasPrefix(line, "Ctrl+C") || !strings.HasSuffix(line, "12 条") || m.cellWidth(line) != 78 {
		t.Fatalf("footer alignment: %q (%d cells)", line, m.cellWidth(line))
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.connectionCancel = cancel
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if ctx.Err() == nil || m.ctx.Err() != nil || m.state != "disconnecting" || m.input.Value() != "saved-token" {
		t.Fatal("Ctrl+C should stop the connection without quitting or clearing its token")
	}
	m.Update(tuiEvent{finished: true})
	if m.state != "disconnected" || m.input.Value() != "saved-token" {
		t.Fatal("disconnection should return to the input with the token retained")
	}
}
