package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestTUIAppleTerminalCellWidths(t *testing.T) {
	m := selectionTUI(t, 80, 24)
	m.legacyCells = true
	// These widths are measured using CPR in the native macOS Terminal.
	for text, want := range map[string]int{
		"A": 1, "中": 2, "─": 1, "e\u0301": 1, "👩‍💻": 5,
		"❤️": 1, "🇨🇳": 2, "👩🏽": 4, "👨‍👩‍👧‍👦": 11, "🏳️‍🌈": 4,
	} {
		if got := m.cellWidth(text); got != want {
			t.Errorf("%q: width=%d, want %d", text, got, want)
		}
	}
	if got := m.cutCells("前👩‍💻后", 2, 7); got != "👩‍💻" {
		t.Fatalf("clipping split the joined emoji: %q", got)
	}
	if got := m.truncateCells("前👩‍💻后", 6, "…"); got != "前…" {
		t.Fatalf("truncation overflowed the physical width: %q", got)
	}
}

func TestTUIAppleTerminalDragUsesPhysicalCells(t *testing.T) {
	m := selectionTUI(t, 80, 24)
	m.legacyCells = true
	m.appendLog("system", "A👩‍💻中文Z\n")
	selectionMouse(m, 4, 2, tea.MouseActionPress) // Inside the five-cell emoji.
	selectionMouse(m, 9, 2, tea.MouseActionMotion)
	selectionMouse(m, 9, 2, tea.MouseActionRelease)
	if text := m.selectedText(); text != "👩‍💻中文" {
		t.Fatalf("physical mouse columns selected %q", text)
	}
	if text := ansi.Strip(m.selectionView()); !strings.HasPrefix(text, "A👩‍💻中文Z\n") {
		t.Fatalf("highlight lost or duplicated text: %q", text)
	}
}

func TestTUIAppleTerminalFramesAfterFoldScrollAndSelection(t *testing.T) {
	m := selectionTUI(t, 80, 24)
	m.legacyCells = true
	var output strings.Builder
	for i := 1; i <= 120; i++ {
		fmt.Fprintf(&output, "[%03d/120] 跨屏选择测试：中文与 emoji 👩‍💻 保持完整。\n", i)
	}
	m.appendLog("output", output.String())
	assertFrame := func() {
		t.Helper()
		lines := strings.Split(m.View(), "\n")
		if len(lines) != 23 || !strings.Contains(lines[0], "Agenterm") {
			t.Fatalf("frame lost its header or changed height: %d rows", len(lines))
		}
		for i, line := range lines {
			if width := m.cellWidth(line); width > 80 {
				t.Fatalf("physical row %d wraps at %d cells: %q", i, width, line)
			}
		}
		if strings.Count(ansi.Strip(m.View()), strings.Repeat("─", 78)) != 2 {
			t.Fatal("frame lost a divider")
		}
	}
	assertFrame()
	clickTUILog(t, m, "▸")
	assertFrame()
	m.Update(tea.MouseMsg{X: 0, Y: 5, Button: tea.MouseButtonWheelDown})
	selectionMouse(m, 0, 4, tea.MouseActionPress)
	selectionMouse(m, 20, 20, tea.MouseActionMotion)
	m.Update(tuiSelectionTick(m.selectionID))
	assertFrame()
	selectionMouse(m, 20, 20, tea.MouseActionRelease)
	assertFrame()
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	// Collapse while viewing the middle of the expanded block.
	selectionMouse(m, 0, 4, tea.MouseActionPress)
	selectionMouse(m, 0, 4, tea.MouseActionRelease)
	assertFrame()
	if strings.Contains(m.selectionView(), "[002/120]") {
		t.Fatal("collapsed frame retained expanded rows")
	}
}

func TestTUIAppleTerminalWrapAndCopy(t *testing.T) {
	m := selectionTUI(t, 12, 20)
	m.legacyCells = true
	m.appendLog("system", "abc👩‍💻def👩‍💻ghi\nlast")
	for _, line := range m.logRows {
		if m.cellWidth(line) > 10 {
			t.Fatalf("wrapped line exceeds its physical width: %q", line)
		}
	}
	selectionMouse(m, 0, 2, tea.MouseActionPress)
	selectionMouse(m, 3, 4, tea.MouseActionMotion)
	selectionMouse(m, 3, 4, tea.MouseActionRelease)
	if text := m.selectedText(); text != "abc👩‍💻def👩‍💻ghi\nlast" {
		t.Fatalf("copy lost a grapheme or source newline: %q; rows=%q", text, m.logRows)
	}
}
