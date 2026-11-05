package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func selectionTUI(t *testing.T, width, height int) *tuiModel {
	t.Helper()
	m := newTUI("")
	m.legacyCells = false // The standard cell model is independent of the test host.
	t.Cleanup(m.cancel)
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m.Update(tuiEvent{connected: true})
	m.clipboard.write = func(context.Context, string) (string, error) {
		t.Error("test must explicitly stub clipboard before executing a copy command")
		return "", nil
	}
	return m
}

func selectionMouse(m *tuiModel, x, y int, action tea.MouseAction) tea.Cmd {
	_, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Action: action, Button: tea.MouseButtonLeft})
	return cmd
}

func TestTUIClickWaitsForReleaseAndDragPreservesFolds(t *testing.T) {
	m := selectionTUI(t, 80, 20)
	m.appendLog("output", "first\nsecond\nthird\n")
	before := m.viewport.View()
	selectionMouse(m, 2, 2, tea.MouseActionPress)
	if m.viewport.View() != before {
		t.Fatal("press toggled the fold before the gesture was known")
	}
	selectionMouse(m, 6, 2, tea.MouseActionMotion)
	cmd := selectionMouse(m, 6, 2, tea.MouseActionRelease)
	if cmd == nil || m.selectedText() != "first" || m.viewport.View() != before || m.spans[0].expanded {
		t.Fatalf("drag changed folding or selected the wrong text: %q", m.selectedText())
	}
	var copied string
	m.clipboard.write = func(_ context.Context, text string) (string, error) {
		copied = text
		return "tui.selection.copied", nil
	}
	m.Update(cmd())
	if copied != "first" || !strings.Contains(ansi.Strip(m.View()), "已复制") {
		t.Fatalf("release did not copy selected text: %q", copied)
	}
	if strings.Contains(m.View(), "选择模式") || strings.Count(ansi.Strip(m.View()), strings.Repeat("─", 78)) != 2 {
		t.Fatal("selection mode survived or a divider disappeared")
	}
	// Clicking a folded block after selecting still expands it in one click.
	clickTUILog(t, m, "▸ first")
	if !m.spans[0].expanded {
		t.Fatal("click-to-expand did not work after selection")
	}
}

func TestTUISelectionGraphemesAndReverseDrag(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		m := selectionTUI(t, 80, 12)
		m.appendLog("system", "A中文👩‍💻e\u0301Z\n")
		start, end := 2, 7 // Include the second cells of 中/emoji and combining accent.
		if reverse {
			start, end = end, start
		}
		selectionMouse(m, start, 2, tea.MouseActionPress)
		selectionMouse(m, end, 2, tea.MouseActionMotion)
		selectionMouse(m, end, 2, tea.MouseActionRelease)
		if text := m.selectedText(); text != "中文👩‍💻e\u0301" {
			t.Fatalf("reverse=%v: grapheme split: %q", reverse, text)
		}
		for _, line := range strings.Split(m.View(), "\n") {
			if ansi.StringWidth(line) > 80 {
				t.Fatal("highlight widened the screen")
			}
		}
	}
}

func TestTUISelectionVisibleWithoutColor(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(profile)
	m := selectionTUI(t, 40, 12)
	m.appendLog("system", "select me\n")
	selectionMouse(m, 0, 2, tea.MouseActionPress)
	selectionMouse(m, 5, 2, tea.MouseActionMotion)
	if !strings.Contains(m.View(), "\x1b[7mselect\x1b[27m") {
		t.Fatal("NO_COLOR made the active selection invisible")
	}
}

func TestTUISelectionEdgeScrollAndStaleTicks(t *testing.T) {
	m := selectionTUI(t, 30, 10)
	for i := 0; i < 50; i++ {
		m.appendLog("system", "history row\n")
	}
	m.viewport.GotoTop()
	selectionMouse(m, 0, 3, tea.MouseActionPress)
	cmd := selectionMouse(m, 10, 6, tea.MouseActionMotion)
	if cmd == nil {
		t.Fatal("bottom edge did not schedule scrolling")
	}
	id := m.selectionID
	for i := 0; i < 12; i++ {
		m.Update(tuiSelectionTick(id))
	}
	if m.viewport.YOffset != 12 || m.selection.anchor.row != 1 || m.selection.cursor.row < 12 {
		t.Fatalf("selection did not span screens: offset=%d, %+v", m.viewport.YOffset, m.selection)
	}
	if strings.Count(m.selectedText(), "\n") < m.viewport.Height {
		t.Fatal("copied text omitted rows outside the viewport")
	}
	// Returning to the middle stops the timer; the top edge scrolls upward.
	selectionMouse(m, 10, 4, tea.MouseActionMotion)
	m.Update(tuiSelectionTick(id))
	if m.viewport.YOffset != 12 {
		t.Fatal("scroll continued away from the edge")
	}
	selectionMouse(m, 0, 2, tea.MouseActionMotion)
	m.Update(tuiSelectionTick(id))
	if m.viewport.YOffset != 11 {
		t.Fatal("top edge did not scroll up")
	}
	selectionMouse(m, 0, 0, tea.MouseActionRelease)
	m.Update(tuiSelectionTick(id))
	if m.viewport.YOffset != 11 || m.selection.down {
		t.Fatal("release outside the log did not stop scrolling")
	}
	m.clearSelection()
	selectionMouse(m, 0, 3, tea.MouseActionPress)
	selectionMouse(m, 10, 6, tea.MouseActionMotion)
	m.Update(tuiSelectionTick(id))
	if m.viewport.YOffset != 11 {
		t.Fatal("old timer scrolled a new gesture")
	}
}

func TestTUISelectionWheelExtendsHeldGesture(t *testing.T) {
	m := selectionTUI(t, 30, 10)
	m.appendLog("system", strings.Repeat("history\n", 40))
	m.viewport.GotoTop()
	selectionMouse(m, 0, 3, tea.MouseActionPress)
	selectionMouse(m, 5, 4, tea.MouseActionMotion)
	m.Update(tea.MouseMsg{X: 5, Y: 4, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.viewport.YOffset != 3 || m.selection.cursor.row != 5 || m.selection.anchor.row != 1 {
		t.Fatalf("wheel failed to extend anchored selection: %+v", m.selection)
	}
}

func TestTUISelectionStreamingKeepsLayoutAndCollectsOutput(t *testing.T) {
	m := selectionTUI(t, 60, 12)
	m.appendLog("output", "first\nsecond\n")
	clickTUILog(t, m, "▸ first")
	before := m.viewport.View()
	selectionMouse(m, 2, 2, tea.MouseActionPress)
	selectionMouse(m, 6, 2, tea.MouseActionMotion)
	m.appendLog("output", "third\n")
	latency := 33 * time.Millisecond
	m.Update(tuiEvent{latency: &latency})
	if m.viewport.View() != before || !strings.Contains(m.logText(), "third") || !strings.Contains(m.View(), "33ms") {
		t.Fatal("streaming moved the selection, lost output, or froze the header")
	}
	selectionMouse(m, 6, 2, tea.MouseActionRelease)
	if m.selectedText() != "first" {
		t.Fatal("streaming changed selected text")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.selection != nil || !m.spans[0].expanded || !strings.Contains(m.viewport.View(), "third") {
		t.Fatal("clearing selection lost layout or pending output")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if m.selection != nil {
		t.Fatal("s still enables selection mode")
	}
}

func TestTUISelectionJoinsSoftWrapsAndKeepsNewlines(t *testing.T) {
	m := selectionTUI(t, 12, 20)
	m.appendLog("system", "abcdefghijk\n中文世界你好\nlast")
	selectionMouse(m, 0, 2, tea.MouseActionPress)
	selectionMouse(m, 3, 6, tea.MouseActionMotion)
	selectionMouse(m, 3, 6, tea.MouseActionRelease)
	if text := m.selectedText(); text != "abcdefghijk\n中文世界你好\nlast" {
		t.Fatalf("copy changed source line breaks: %q; rows=%q soft=%v", text, m.logRows, m.softWraps)
	}
}

func TestTUISelectionClipboardFailureAndNewestRequestWins(t *testing.T) {
	m := selectionTUI(t, 40, 12)
	m.appendLog("system", "first second\n")
	selectionMouse(m, 0, 2, tea.MouseActionPress)
	selectionMouse(m, 4, 2, tea.MouseActionMotion)
	old := selectionMouse(m, 4, 2, tea.MouseActionRelease)
	var writes []string
	m.clipboard.write = func(_ context.Context, text string) (string, error) {
		writes = append(writes, text)
		return "", errors.New("clipboard unavailable")
	}
	_, latest := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m.Update(latest())
	if !strings.Contains(m.View(), "复制失败") || m.ctx.Err() != nil {
		t.Fatal("copy failure was hidden or Ctrl+C exited with an active selection")
	}
	if old() != nil || len(writes) != 1 || writes[0] != "first" {
		t.Fatalf("stale clipboard request overwrote the latest: %q", writes)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.ctx.Err() != nil || m.state != "disconnecting" {
		t.Fatal("Ctrl+C without a selection should disconnect")
	}
}

func TestTUISelectionResizeDisconnectAndHistoryEviction(t *testing.T) {
	m := selectionTUI(t, 80, 20)
	m.appendLog("system", "old text\n")
	selectionMouse(m, 0, 2, tea.MouseActionPress)
	selectionMouse(m, 3, 2, tea.MouseActionMotion)
	id := m.selectionID
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 10})
	m.Update(tuiSelectionTick(id))
	if m.selection != nil {
		t.Fatal("resize retained invalid selection coordinates")
	}
	selectionMouse(m, 0, 2, tea.MouseActionPress)
	selectionMouse(m, 3, 2, tea.MouseActionMotion)
	m.logBytes = maxTUILog
	m.spans = []logSpan{{kind: "output", text: strings.Repeat("x", maxTUILog)}}
	m.appendLog("system", "new\n")
	if m.selection != nil || len(m.logText()) > maxTUILog {
		t.Fatal("history eviction retained a stale selection")
	}
	m.Update(tuiEvent{finished: true})
	if m.selection != nil || !strings.Contains(ansi.Strip(m.View()), "连接 Token") {
		t.Fatal("disconnect did not restore connection form")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if m.input.Value() != "s" {
		t.Fatal("s was captured instead of entering the token")
	}
}
