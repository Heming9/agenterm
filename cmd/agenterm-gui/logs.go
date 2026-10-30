package main

import (
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

const maxLogBytes = 64 * 1024

func logTail(text string) string {
	if len(text) > maxLogBytes {
		text = text[len(text)-maxLogBytes:]
		for len(text) > 0 && !utf8.RuneStart(text[0]) {
			text = text[1:]
		}
	}
	return text
}

// Each connection owns a buffer so a finishing old process cannot write into a
// newly connected session. Pending and displayed output are both bounded.
type logBuffer struct {
	mu   sync.Mutex
	data string
}

var _ io.Writer = (*logBuffer)(nil)

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = logTail(b.data + string(p))
	return len(p), nil
}

func (b *logBuffer) take() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := b.data
	b.data = ""
	return text
}

// The GUI is a log viewer, not a terminal emulator. Escape state spans chunks.
type displayDecoder struct {
	state   byte
	pending string
}

func (d *displayDecoder) text(chunk string) string {
	chunk = d.pending + chunk
	d.pending = ""
	var output strings.Builder
	for len(chunk) > 0 {
		if !utf8.FullRuneInString(chunk) {
			d.pending = chunk
			break
		}
		r, size := utf8.DecodeRuneInString(chunk)
		chunk = chunk[size:]
		switch d.state {
		case 1:
			switch r {
			case '[':
				d.state = 2
			case ']':
				d.state = 3
			default:
				d.state = 0
			}
		case 2:
			if r >= 0x40 && r <= 0x7e {
				d.state = 0
			}
		case 3:
			if r == 7 {
				d.state = 0
			} else if r == 27 {
				d.state = 4
			}
		case 4:
			if r == '\\' {
				d.state = 0
			} else {
				d.state = 3
			}
		default:
			if r == 27 {
				d.state = 1
			} else if r == '\n' || r == '\t' || r >= 32 && r != 127 {
				output.WriteRune(r)
			}
		}
	}
	return output.String()
}
