package main

import (
	"strings"
	"testing"
	"time"

	"agenterm/internal/client"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestTUISelectionReleasesMouseAndPreservesIncomingOutput(t *testing.T) {
	m := newTUI("")
	defer m.cancel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.Update(tuiEvent{connected: true})
	m.Update(tuiEvent{command: &client.CommandEvent{ID: "one", Command: "echo first\necho second"}})
	m.Update(tuiEvent{id: "one", output: []byte("first\nsecond\n")})
	clickTUILog(t, m, "▸ first")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if cmd == nil || !m.selecting {
		t.Fatal("selection mode must hand control to the terminal")
	}
	frozen := m.View()
	if !strings.Contains(ansi.Strip(frozen), "选择模式") {
		t.Fatalf("selection mode lost its return instructions:\n%s", frozen)
	}
	m.Update(tuiEvent{id: "one", output: []byte("third\n")})
	delay := 50 * time.Millisecond
	m.Update(tuiEvent{latency: &delay})
	// Queued mouse input and scrolling must not disturb a native selection.
	m.Update(tea.MouseMsg{X: 0, Y: 2, Button: tea.MouseButtonLeft})
	m.Update(tea.MouseMsg{X: 0, Y: 3, Button: tea.MouseButtonWheelDown})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if m.View() != frozen || !strings.Contains(m.logs, "third") {
		t.Fatal("selection changed on incoming output or output was lost")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if cmd == nil || m.selecting {
		t.Fatal("returning from selection mode must restore mouse interaction")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "third") || !strings.Contains(view, "50ms") || strings.Contains(view, tr("tui.selection.help")) {
		t.Fatalf("returning should show buffered output and current status:\n%s", view)
	}
	clickTUILog(t, m, "▾ first")
	if !strings.Contains(ansi.Strip(m.viewport.View()), "▸ first") {
		t.Fatal("click to collapse was not restored")
	}
}

func TestTUISelectionRetainsScrollPositionAndExpansion(t *testing.T) {
	m := newTUI("")
	defer m.cancel()
	m.Update(tuiEvent{connected: true})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	m.appendLog("output", strings.Repeat("earlier output\n", 20))
	m.appendLog("system", "\nnext command\n")
	m.appendLog("output", "later output\nsecond line\n")
	clickTUILog(t, m, "▸ later output")
	m.viewport.GotoTop()
	before := m.viewport.View()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m.appendLog("system", "new output while selecting\n")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if m.viewport.YOffset != 0 || m.viewport.View() != before {
		t.Fatal("returning from selection lost the log browsing position")
	}
	m.viewport.GotoBottom()
	if !strings.Contains(ansi.Strip(m.viewport.View()), "▾ later output") {
		t.Fatal("selection mode changed the expansion state")
	}
}

func TestTUISelectionResizeAndDisconnect(t *testing.T) {
	m := newTUI("")
	defer m.cancel()
	m.Update(tuiEvent{connected: true})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.appendLog("output", "visible output\n")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 10})
	view := m.View()
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 30 {
			t.Fatalf("selection screen overflowed after resize: %q", line)
		}
	}
	if !strings.Contains(ansi.Strip(view), "选择模式") {
		t.Fatal("resize exited selection mode")
	}
	m.Update(tuiEvent{finished: true})
	if m.selecting || m.selectionView != "" || !strings.Contains(ansi.Strip(m.View()), "连接 Token") {
		t.Fatal("disconnect left a stale selection screen instead of showing reconnection input")
	}
	// The selection shortcut must still be a normal token character when offline.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if m.input.Value() != "s" || m.selecting {
		t.Fatal("selection shortcut captured disconnected token input")
	}
}
