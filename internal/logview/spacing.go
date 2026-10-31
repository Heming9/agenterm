// Package logview formats command logs without changing captured process output.
package logview

import "strings"

// Spacing tracks the displayed tail even when the GUI drains its pending buffer.
type Spacing struct {
	content  bool
	newlines int
}

func (s *Spacing) Observe(text string) {
	if text == "" {
		return
	}
	s.content = true
	tail := len(text) - len(strings.TrimRight(text, "\n"))
	if tail == len(text) {
		s.newlines += tail
	} else {
		s.newlines = tail
	}
}

// Before returns the missing line breaks; an empty log needs no separator.
func (s *Spacing) Before(newlines int) string {
	if !s.content {
		return ""
	}
	return strings.Repeat("\n", max(0, newlines-s.newlines))
}

// TrimLeadingBlankLines starts resumed output directly below its source label,
// preserving indentation on the first line that contains text.
func TrimLeadingBlankLines(text string) string {
	for {
		end := strings.IndexByte(text, '\n')
		if end < 0 || strings.Trim(text[:end], " \t") != "" {
			return text
		}
		text = text[end+1:]
	}
}

// Output removes blank lines at the edges of a command's output. The first
// newline is displayed immediately; additional blank lines wait for more text,
// so interior spacing survives arbitrary output chunk boundaries.
type Output struct {
	started     bool
	lineContent bool
	pending     []byte
}

func (o *Output) Text(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch {
		case ch == '\n' && o.lineContent:
			out.WriteByte(ch)
			o.lineContent = false
		case ch == '\n' && !o.started:
			o.pending = o.pending[:0]
		case ch == '\n' || !o.lineContent && (ch == ' ' || ch == '\t'):
			// Match the viewers' bounded logs, including whitespace-only output.
			if len(o.pending) < 64*1024 {
				o.pending = append(o.pending, ch)
			}
		default:
			out.Write(o.pending)
			o.pending = o.pending[:0]
			out.WriteByte(ch)
			o.started = true
			o.lineContent = true
		}
	}
	return out.String()
}
