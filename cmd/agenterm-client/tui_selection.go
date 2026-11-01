package main

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/rivo/uniseg"
)

// Coordinates refer to rendered log rows, never to the moving screen viewport.
type tuiPoint struct{ row, col int }

type tuiSelection struct {
	anchor, cursor         tuiPoint
	down, dragged, ticking bool
	mouseX, mouseY         int
	notice                 string
}

type tuiSelectionTick uint64

var tuiSelectionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Background(lipgloss.Color("#35688a"))

func (m *tuiModel) clearSelection() {
	m.selection = nil
	if m.logDirty {
		// Refresh pending output without pulling the reader back to the bottom.
		offset := m.viewport.YOffset
		m.refreshLog()
		m.viewport.SetYOffset(offset)
	}
}

func (m *tuiModel) insideLog(x, y int) bool {
	return m.state != "disconnected" && x >= 0 && x < m.viewport.Width && y >= 2 && y < 2+m.viewport.Height
}

func (m *tuiModel) logPoint(x, y int) tuiPoint {
	row := m.viewport.YOffset + min(max(0, y-2), m.viewport.Height-1)
	return tuiPoint{min(row, max(0, len(m.logRows)-1)), min(max(0, x), m.viewport.Width-1)}
}

func (m *tuiModel) handleLogMouse(msg tea.MouseMsg) tea.Cmd {
	if m.state == "disconnected" {
		return nil
	}
	s := m.selection
	if tea.MouseEvent(msg).IsWheel() {
		if !m.insideLog(msg.X, msg.Y) {
			return nil
		}
		m.viewport, _ = m.viewport.Update(msg)
		if s != nil && s.down {
			s.dragged = true
			s.cursor = m.logPoint(s.mouseX, s.mouseY)
		}
		return nil
	}
	switch msg.Action {
	case tea.MouseActionPress:
		if msg.Button != tea.MouseButtonLeft {
			return nil
		}
		if !m.insideLog(msg.X, msg.Y) || len(m.logRows) == 0 {
			m.clearSelection()
			return nil
		}
		// Replace any previous selection in-place. Pending output must not
		// reflow the content under this press before its gesture is complete.
		m.selectionID++
		p := m.logPoint(msg.X, msg.Y)
		m.selection = &tuiSelection{anchor: p, cursor: p, down: true, mouseX: msg.X, mouseY: msg.Y}
	case tea.MouseActionMotion:
		if s == nil || !s.down {
			return nil
		}
		s.mouseX, s.mouseY = msg.X, msg.Y
		s.cursor = m.logPoint(msg.X, msg.Y)
		if s.cursor != s.anchor || !m.insideLog(msg.X, msg.Y) {
			s.dragged = true
		}
		return m.selectionTick()
	case tea.MouseActionRelease:
		// X10 releases use ButtonNone; SGR releases preserve the button.
		if s == nil || !s.down || (msg.Button != tea.MouseButtonLeft && msg.Button != tea.MouseButtonNone) {
			return nil
		}
		s.cursor = m.logPoint(msg.X, msg.Y)
		s.dragged = s.dragged || s.cursor != s.anchor
		s.down = false
		if s.dragged {
			return m.copySelection()
		}
		row := s.anchor.row
		m.selection = nil
		if m.insideLog(msg.X, msg.Y) {
			m.toggleFold(row)
		}
		if m.logDirty {
			m.refreshLog()
		}
	}
	return nil
}

func (m *tuiModel) selectionDirection() int {
	s := m.selection
	if s == nil || !s.down || !s.dragged {
		return 0
	}
	if s.mouseY <= 2 {
		return -1
	}
	if s.mouseY >= m.viewport.Height+1 {
		return 1
	}
	return 0
}

func (m *tuiModel) selectionTick() tea.Cmd {
	if m.selectionDirection() == 0 || m.selection.ticking {
		return nil
	}
	m.selection.ticking = true
	id := m.selectionID
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return tuiSelectionTick(id) })
}

func (m *tuiModel) scrollSelection(id uint64) tea.Cmd {
	if m.selection == nil || id != m.selectionID {
		return nil
	}
	m.selection.ticking = false
	direction := m.selectionDirection()
	if direction == 0 {
		return nil
	}
	before := m.viewport.YOffset
	m.viewport.SetYOffset(before + direction)
	m.selection.cursor = m.logPoint(m.selection.mouseX, m.selection.mouseY)
	if m.viewport.YOffset == before {
		return nil
	}
	return m.selectionTick()
}

// Snap both ends to whole grapheme clusters, including wide CJK/emoji cells.
func (m *tuiModel) selectionCell(line string, col int, end bool) int {
	g := uniseg.NewGraphemes(ansi.Strip(line))
	x := 0
	for g.Next() {
		next := x + m.clusterWidth(g.Str())
		if col < next {
			if end {
				return next
			}
			return x
		}
		x = next
	}
	return x
}

func (m *tuiModel) selectionColumns(row int) (int, int) {
	s := m.selection
	if s == nil || !s.dragged || row < 0 || row >= len(m.logRows) {
		return 0, 0
	}
	a, b := s.anchor, s.cursor
	if a.row > b.row || (a.row == b.row && a.col > b.col) {
		a, b = b, a
	}
	if row < a.row || row > b.row {
		return 0, 0
	}
	line := m.logRows[row]
	left, right := 0, m.cellWidth(line)
	if row == a.row {
		left = m.selectionCell(line, a.col, false)
	}
	if row == b.row {
		right = m.selectionCell(line, b.col, true)
	}
	return left, right
}

func (m *tuiModel) selectedText() string {
	if m.selection == nil || !m.selection.dragged {
		return ""
	}
	a, b := m.selection.anchor.row, m.selection.cursor.row
	if a > b {
		a, b = b, a
	}
	var text strings.Builder
	for row := a; row <= b && row < len(m.logRows); row++ {
		left, right := m.selectionColumns(row)
		text.WriteString(ansi.Strip(m.cutCells(m.logRows[row], left, right)))
		if row < b && !m.softWraps[row] {
			text.WriteByte('\n')
		}
	}
	return text.String()
}

func (m *tuiModel) selectionView() string {
	// Leave short lines unpadded. Terminal emulators disagree on some Unicode
	// widths; padding with a different width model can silently wrap the screen.
	lines := make([]string, m.viewport.Height)
	for i := range lines {
		row := m.viewport.YOffset + i
		if row < len(m.logRows) {
			lines[i] = m.cutCells(m.logRows[row], 0, m.viewport.Width)
		}
	}
	if m.selection == nil || !m.selection.dragged {
		return strings.Join(lines, "\n")
	}
	for i, line := range lines {
		left, right := m.selectionColumns(m.viewport.YOffset + i)
		if right > left {
			selected := ansi.Strip(m.cutCells(line, left, right))
			if lipgloss.ColorProfile() == termenv.Ascii {
				// Selection must remain visible even when NO_COLOR disables colors.
				selected = "\x1b[7m" + selected + "\x1b[27m"
			} else {
				selected = tuiSelectionStyle.Render(selected)
			}
			lines[i] = m.cutCells(line, 0, left) + selected + m.cutCells(line, right, m.viewport.Width)
		}
	}
	return strings.Join(lines, "\n")
}
