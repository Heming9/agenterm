package main

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// Terminal.app measures joined emoji as individual code points. In particular,
// its ZWJ occupies one cell, while variation selectors and accents remain zero
// width. Keep graphemes intact for copying, but use the terminal's actual cells
// for wrapping, clipping and mouse coordinates.
func (m *tuiModel) clusterWidth(cluster string) int {
	if !m.legacyCells {
		return uniseg.StringWidth(cluster)
	}
	width := 0
	for _, r := range cluster {
		switch {
		case r == '\u200d', r >= '\U0001f1e6' && r <= '\U0001f1ff':
			width++
		case r >= '\U0001f3fb' && r <= '\U0001f3ff':
			width += 2
		default:
			width += uniseg.StringWidth(string(r))
		}
	}
	return width
}

func (m *tuiModel) cellWidth(text string) int {
	if !m.legacyCells {
		return ansi.StringWidth(text)
	}
	width := 0
	g := uniseg.NewGraphemes(ansi.Strip(text))
	for g.Next() {
		width += m.clusterWidth(g.Str())
	}
	return width
}

func (m *tuiModel) cutCells(text string, left, right int) string {
	if !m.legacyCells {
		return ansi.Cut(text, left, right)
	}
	if right <= left {
		return ""
	}
	var out strings.Builder
	column := 0
	var state byte
	for len(text) > 0 {
		seq, cells, n, next := ansi.DecodeSequence(text, state, nil)
		text, state = text[n:], next
		if cells > 0 || (len(seq) > 0 && seq[0] >= 0xc0) {
			cells = m.clusterWidth(seq)
			if column >= left && column+cells <= right {
				out.WriteString(seq)
			}
			column += cells
		} else {
			// Keep style changes on either side of the selected text.
			out.WriteString(seq)
		}
	}
	return out.String()
}

func (m *tuiModel) truncateCells(text string, width int, tail string) string {
	if !m.legacyCells {
		return ansi.Truncate(text, width, tail)
	}
	if m.cellWidth(text) <= width {
		return text
	}
	if m.cellWidth(tail) > width {
		return ""
	}
	return m.cutCells(text, 0, width-m.cellWidth(tail)) + tail
}

func (m *tuiModel) wrapCells(text string, width int) string {
	if !m.legacyCells {
		return ansi.Hardwrap(text, width, true)
	}
	var out strings.Builder
	column := 0
	var state byte
	for len(text) > 0 {
		seq, cells, n, next := ansi.DecodeSequence(text, state, nil)
		text, state = text[n:], next
		if cells > 0 || (len(seq) > 0 && seq[0] >= 0xc0) {
			cells = m.clusterWidth(seq)
			if cells > width {
				// A grapheme wider than the entire viewport cannot be drawn safely.
				seq, cells = "…", 1
			}
			if column+cells > width {
				out.WriteByte('\n')
				column = 0
			}
			column += cells
		}
		out.WriteString(seq)
	}
	return out.String()
}
