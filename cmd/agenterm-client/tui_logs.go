package main

import (
	"fmt"
	"strings"
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

func (m *tuiModel) refreshLog() {
	if m.selection != nil {
		m.logDirty = true
		return
	}
	m.logDirty = false
	atBottom := m.viewport.AtBottom()
	width := max(1, m.viewport.Width)
	m.folds = nil
	m.softWraps = make(map[int]bool)
	var b strings.Builder
	row := 0
	for i := 0; i < len(m.spans); i++ {
		span := &m.spans[i]
		prefix := ""
		// Keep the shell label and command together when wrapping and folding.
		if span.kind == "system" && !strings.HasSuffix(span.text, "\n") && i+1 < len(m.spans) && m.spans[i+1].kind == "command" {
			prefix = span.text
			i++
			span = &m.spans[i]
		}
		text := prefix + span.text
		body := strings.Trim(text, "\n")
		if body == "" {
			b.WriteString(text)
			row += strings.Count(text, "\n")
			continue
		}
		leading := len(text) - len(strings.TrimLeft(text, "\n"))
		trailing := len(text) - len(strings.TrimRight(text, "\n"))
		b.WriteString(strings.Repeat("\n", leading))
		row += leading
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
			m.folds = append(m.folds, tuiFold{span: i, start: row, end: row + strings.Count(wrapped, "\n") + 1})
		}
		for _, line := range soft {
			m.softWraps[row+line] = true
		}
		b.WriteString(wrapped)
		b.WriteString(strings.Repeat("\n", trailing))
		row += strings.Count(wrapped, "\n") + trailing
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
