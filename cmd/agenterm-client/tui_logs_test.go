package main

import (
	"strings"
	"testing"

	"agenterm/internal/client"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func clickTUILog(t *testing.T, m *tuiModel, preview string) {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(m.viewport.View()), "\n") {
		if strings.Contains(line, preview) {
			m.Update(tea.MouseMsg{X: 0, Y: y + 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
			return
		}
	}
	t.Fatalf("preview %q is not visible in:\n%s", preview, ansi.Strip(m.View()))
}

func TestTUIMultilineCommandAndOutputToggleIndependently(t *testing.T) {
	m := newTUI("")
	defer m.cancel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.Update(tuiEvent{connected: true})
	m.Update(tuiEvent{command: &client.CommandEvent{ID: "one", Command: "echo first\necho second\necho third"}})
	// Chunk boundaries must not produce separate output previews.
	for _, chunk := range []string{"first\n", "sec", "ond\n"} {
		m.Update(tuiEvent{id: "one", output: []byte(chunk)})
	}
	view := ansi.Strip(m.viewport.View())
	if strings.Count(view, "▸") != 2 || strings.Contains(view, "second") {
		t.Fatalf("multiline content should start collapsed:\n%s", view)
	}
	clickTUILog(t, m, "s1")
	view = ansi.Strip(m.viewport.View())
	if !strings.Contains(view, "echo second") || !strings.Contains(view, "▸ first") {
		t.Fatalf("command should expand independently:\n%s", view)
	}
	clickTUILog(t, m, "▸ first")
	m.Update(tuiEvent{id: "one", output: []byte("third\n")})
	m.Update(tuiEvent{command: &client.CommandEvent{ID: "one", Done: true}})
	view = ansi.Strip(m.viewport.View())
	if !strings.Contains(view, "▾ first") || !strings.Contains(view, "third") || strings.Contains(view, "▸") {
		t.Fatalf("streaming and completion should preserve expansion:\n%s", view)
	}
	raw := m.logs
	clickTUILog(t, m, "▾ first")
	view = ansi.Strip(m.viewport.View())
	if !strings.Contains(view, "▸ first") || !strings.Contains(view, "echo second") || strings.Count(view, "third") != 1 {
		t.Fatalf("only the output should collapse again:\n%s", view)
	}
	if m.logs != raw || !strings.Contains(raw, "first\nsecond\nthird\n") {
		t.Fatalf("folding changed captured logs: %q", m.logs)
	}
}

func TestTUISingleLineAndSystemMessagesStayVisible(t *testing.T) {
	m := newTUI("")
	defer m.cancel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.Update(tuiEvent{connected: true})
	m.appendLog("system", "connection message\n")
	m.Update(tuiEvent{command: &client.CommandEvent{ID: "one", Command: "echo short"}})
	m.Update(tuiEvent{id: "one", output: []byte("short\n")})
	view := ansi.Strip(m.viewport.View())
	if strings.Contains(view, "▸") || !strings.Contains(view, "connection message") || !strings.Contains(view, "echo short") {
		t.Fatalf("single lines or separate system messages were folded:\n%s", view)
	}
	clickTUILog(t, m, "echo short")
	if after := ansi.Strip(m.viewport.View()); after != view {
		t.Fatalf("clicking a single line changed it:\n%s", after)
	}
}

func TestTUIFoldUsesDisplayWidthAndSurvivesResize(t *testing.T) {
	for _, text := range []string{"abcdefghijklmnopqrstuvwxyz", strings.Repeat("中文", 7), "a\tb\tc\td\te\tf"} {
		t.Run(text, func(t *testing.T) {
			m := newTUI("")
			defer m.cancel()
			m.Update(tuiEvent{connected: true})
			m.Update(tea.WindowSizeMsg{Width: 22, Height: 20})
			m.appendLog("output", text+"\n")
			if view := ansi.Strip(m.viewport.View()); !strings.Contains(view, "▸") {
				t.Fatalf("wrapped output should collapse:\n%s", view)
			}
			clickTUILog(t, m, "▸")
			m.Update(tea.WindowSizeMsg{Width: 82, Height: 20})
			view := ansi.Strip(m.viewport.View())
			if !strings.Contains(view, strings.ReplaceAll(text, "\t", "    ")) || strings.ContainsAny(view, "▸▾") {
				t.Fatalf("content should fit on one line after resize:\n%s", view)
			}
			m.Update(tea.WindowSizeMsg{Width: 22, Height: 20})
			if view = ansi.Strip(m.viewport.View()); !strings.Contains(view, "▾") || strings.Contains(view, "▸") {
				t.Fatalf("resize lost expansion state:\n%s", view)
			}
			if m.logs != text+"\n" {
				t.Fatal("display wrapping changed raw text")
			}
		})
	}
}

func TestTUIClickAfterScrollingKeepsExpandedBlockVisible(t *testing.T) {
	m := newTUI("")
	defer m.cancel()
	m.Update(tuiEvent{connected: true})
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 10})
	m.appendLog("system", strings.Repeat("earlier log\n", 10))
	m.appendLog("output", "target\n"+strings.Repeat("hidden\n", 20))
	if m.viewport.YOffset == 0 {
		t.Fatal("fixture should be scrolled")
	}
	offset := m.viewport.YOffset
	clickTUILog(t, m, "▸ target")
	if m.viewport.YOffset != offset || !strings.Contains(ansi.Strip(m.viewport.View()), "▾ target") {
		t.Fatalf("expansion jumped away from the clicked row:\n%s", ansi.Strip(m.viewport.View()))
	}
	m.Update(tea.MouseMsg{X: 0, Y: 3, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.viewport.YOffset <= offset {
		t.Fatal("mouse wheel did not scroll expanded output")
	}
	clickTUILog(t, m, "hidden")
	if !strings.Contains(ansi.Strip(m.viewport.View()), "▸ target") {
		t.Fatalf("collapsing scrolled output lost its header:\n%s", ansi.Strip(m.viewport.View()))
	}
}

func TestTUIFoldMouseBoundsAndClear(t *testing.T) {
	m := newTUI("")
	defer m.cancel()
	m.Update(tuiEvent{connected: true})
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m.appendLog("output", "first\nsecond\n")
	before := m.viewport.View()
	for _, mouse := range []tea.MouseMsg{
		{X: 0, Y: 1, Button: tea.MouseButtonLeft},
		{X: 0, Y: 2 + m.viewport.Height, Button: tea.MouseButtonLeft},
		{X: m.viewport.Width, Y: 2, Button: tea.MouseButtonLeft},
		{X: 0, Y: 2, Button: tea.MouseButtonRight},
		{X: 0, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease},
		{X: 0, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion},
	} {
		m.Update(mouse)
		if m.viewport.View() != before {
			t.Fatalf("unexpected toggle for %+v", mouse)
		}
	}
	clickTUILog(t, m, "▸ first")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m.Update(tea.MouseMsg{X: 0, Y: 2, Button: tea.MouseButtonLeft})
	if m.logs != "" || len(m.folds) != 0 || m.viewport.YOffset != 0 {
		t.Fatal("clear retained stale folding state")
	}
	m.appendLog("output", "fresh\nnew\n")
	if view := ansi.Strip(m.viewport.View()); !strings.Contains(view, "▸ fresh") || strings.Contains(view, "new") {
		t.Fatalf("fresh output should start collapsed:\n%s", view)
	}
}
