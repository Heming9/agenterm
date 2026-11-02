package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func styledLogText(kind, text string) string {
	// Match Lip Gloss's tab expansion before measuring output rows as well.
	text = strings.ReplaceAll(text, "\t", "    ")
	style, ok := logStyles[kind]
	if !ok {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = style.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

// A layout is reusable until its text, expansion, or display settings change.
// Text keys share the source strings; rendered content is bounded by log history.
type tuiLogLayout struct {
	text, prefix, kind     string
	expanded               bool
	content                string
	rows, leading, foldEnd int
	soft                   []int
}

type tuiLogLayoutStyle struct {
	width        int
	legacy, dark bool
	profile      termenv.Profile
	language     string
}

func (m *tuiModel) refreshLog() {
	if m.selection != nil {
		m.logDirty = true
		return
	}
	m.logDirty = false
	atBottom := m.viewport.AtBottom()
	width := max(1, m.viewport.Width)
	style := tuiLogLayoutStyle{width: width, legacy: m.legacyCells, dark: lipgloss.HasDarkBackground(), profile: lipgloss.ColorProfile(), language: cliLanguage}
	if style != m.logLayoutStyle {
		for i := range m.spans {
			m.spans[i].layout = nil
		}
		m.logLayoutStyle = style
	}
	m.folds = nil
	m.softWraps = make(map[int]bool)
	var b strings.Builder
	row := 0
	for i := 0; i < len(m.spans); i++ {
		span := &m.spans[i]
		prefix := ""
		// Keep the timestamp and command together when wrapping and folding.
		if span.kind == "system" && !strings.HasSuffix(span.text, "\n") && i+1 < len(m.spans) && m.spans[i+1].kind == "command" {
			prefix = span.text
			i++
			span = &m.spans[i]
		}
		layout := m.layoutLogSpan(span, prefix, width)
		if layout.foldEnd > 0 {
			m.folds = append(m.folds, tuiFold{span: i, start: row + layout.leading, end: row + layout.foldEnd})
		}
		for _, line := range layout.soft {
			m.softWraps[row+layout.leading+line] = true
		}
		b.WriteString(layout.content)
		row += layout.rows
	}
	content := b.String()
	m.logRows = strings.Split(content, "\n")
	m.viewport.SetContent(content)
	// SetContent does not clamp offsets that fall into the last screenful.
	m.viewport.SetYOffset(m.viewport.YOffset)
	if atBottom {
		m.viewport.GotoBottom()
	}
}

func (m *tuiModel) layoutLogSpan(span *logSpan, prefix string, width int) *tuiLogLayout {
	if cached := span.layout; cached != nil && cached.text == span.text && cached.prefix == prefix && cached.kind == span.kind && cached.expanded == span.expanded {
		return cached
	}
	layout := &tuiLogLayout{text: span.text, prefix: prefix, kind: span.kind, expanded: span.expanded}
	span.layout = layout
	text := prefix + span.text
	body := strings.Trim(text, "\n")
	if body == "" {
		layout.content, layout.rows = text, strings.Count(text, "\n")
		return layout
	}
	leading := len(text) - len(strings.TrimLeft(text, "\n"))
	trailing := len(text) - len(strings.TrimRight(text, "\n"))
	styled := styledLogText(span.kind, body)
	if prefix != "" {
		styled = styledLogText("system", strings.TrimLeft(prefix, "\n")) + styledLogText(span.kind, strings.TrimRight(span.text, "\n"))
	}
	wrapped, soft := m.wrapTUILog(styled, width)
	lines := strings.Count(wrapped, "\n") + 1
	if (span.kind == "command" || span.kind == "output" || span.kind == "error") && lines > 1 {
		if span.expanded {
			wrapped, soft = m.wrapTUILog("▾ "+styled, width)
		} else {
			first, _, _ := strings.Cut(wrapped, "\n")
			suffix := " " + fmt.Sprintf(tr("tui.fold.lines"), lines)
			// Leave room for the preview even in a narrow terminal.
			if m.cellWidth(suffix)+4 > width {
				suffix = ""
			}
			marker := m.truncateCells("▸ ", width, "")
			previewWidth := max(0, width-m.cellWidth(marker)-m.cellWidth(suffix))
			wrapped = marker + m.truncateCells(first, previewWidth, "…") + styledLogText("system", suffix)
			soft = nil
		}
		layout.foldEnd = leading + strings.Count(wrapped, "\n") + 1
	}
	layout.leading, layout.soft = leading, soft
	layout.content = strings.Repeat("\n", leading) + wrapped + strings.Repeat("\n", trailing)
	layout.rows = leading + strings.Count(wrapped, "\n") + trailing
	return layout
}

// Remember visual wraps so copying joins them without removing real newlines.
func (m *tuiModel) wrapTUILog(text string, width int) (string, []int) {
	lines := strings.Split(text, "\n")
	var soft []int
	row := 0
	for i, line := range lines {
		lines[i] = m.wrapCells(line, width)
		n := strings.Count(lines[i], "\n")
		for j := 0; j < n; j++ {
			soft = append(soft, row+j)
		}
		row += n + 1
	}
	return strings.Join(lines, "\n"), soft
}

func (m *tuiModel) toggleFold(row int) {
	for _, fold := range m.folds {
		if row < fold.start || row >= fold.end {
			continue
		}
		offset := min(m.viewport.YOffset, fold.start)
		m.spans[fold.span].expanded = !m.spans[fold.span].expanded
		m.refreshLog()
		// Keep the clicked block visible, including when expanding at the bottom.
		m.viewport.SetYOffset(offset)
		return
	}
}
