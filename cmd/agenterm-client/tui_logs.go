package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
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
	atBottom := m.viewport.AtBottom()
	width := max(1, m.viewport.Width)
	m.folds = nil
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
		wrapped := ansi.Hardwrap(styled, width, true)
		lines := strings.Count(wrapped, "\n") + 1
		if (span.kind == "command" || span.kind == "output" || span.kind == "error") && lines > 1 {
			if span.expanded {
				wrapped = ansi.Hardwrap("▾ "+styled, width, true)
			} else {
				first, _, _ := strings.Cut(wrapped, "\n")
				suffix := " " + fmt.Sprintf(tr("tui.fold.lines"), lines)
				// Leave room for the preview even in a narrow terminal.
				if ansi.StringWidth(suffix)+4 > width {
					suffix = ""
				}
				marker := ansi.Truncate("▸ ", width, "")
				previewWidth := max(0, width-ansi.StringWidth(marker)-ansi.StringWidth(suffix))
				wrapped = marker + ansi.Truncate(first, previewWidth, "…") + styledLogText("system", suffix)
			}
			m.folds = append(m.folds, tuiFold{span: i, start: row, end: row + strings.Count(wrapped, "\n") + 1})
		}
		b.WriteString(wrapped)
		b.WriteString(strings.Repeat("\n", trailing))
		row += strings.Count(wrapped, "\n") + trailing
	}
	m.viewport.SetContent(b.String())
	// SetContent does not clamp offsets that fall into the last screenful.
	m.viewport.SetYOffset(m.viewport.YOffset)
	if atBottom {
		m.viewport.GotoBottom()
	}
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
