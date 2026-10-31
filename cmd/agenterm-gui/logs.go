package main

import (
	"agenterm/internal/client"
	"agenterm/internal/logview"
	"fmt"
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
	mu       sync.Mutex
	data     string
	spans    []logSpan
	next     int
	last     string
	shells   map[string]int
	decoders map[string]*displayDecoder
	spacing  logview.Spacing
}

type logSpan struct {
	kind string
	text string
}

func (b *logBuffer) append(kind, text string) {
	if text == "" {
		return
	}
	b.spacing.Observe(text)
	b.data = logTail(b.data + text)
	if len(b.spans) > 0 && b.spans[len(b.spans)-1].kind == kind {
		b.spans[len(b.spans)-1].text += text
	} else {
		b.spans = append(b.spans, logSpan{kind, text})
	}
	total := 0
	for _, span := range b.spans {
		total += len(span.text)
	}
	for total > maxLogBytes && len(b.spans) > 0 {
		excess := total - maxLogBytes
		if len(b.spans[0].text) <= excess {
			total -= len(b.spans[0].text)
			b.spans = b.spans[1:]
			continue
		}
		old := b.spans[0].text
		tail := old[excess:]
		for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
			tail = tail[1:]
		}
		b.spans[0].text = tail
		total -= len(old) - len(tail)
	}
}

var _ io.Writer = (*logBuffer)(nil)

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.append("output", string(p))
	return len(p), nil
}

func (b *logBuffer) take() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := b.data
	b.data = ""
	b.spans = nil
	return text
}

func (b *logBuffer) takeSpans() []logSpan {
	b.mu.Lock()
	defer b.mu.Unlock()
	spans := b.spans
	b.data = ""
	b.spans = nil
	return spans
}

// command and output serialize labels with their associated chunks, including
// when several shells write concurrently.
func (b *logBuffer) command(event client.CommandEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if event.Done {
		if event.Error != "" {
			b.switchSource(event.ID)
			b.append("error", b.spacing.Before(1)+event.Error+"\n")
		}
		delete(b.shells, event.ID)
		delete(b.decoders, event.ID)
		return
	}
	if b.shells == nil {
		b.shells = make(map[string]int)
		b.decoders = make(map[string]*displayDecoder)
	}
	b.next++
	b.shells[event.ID] = b.next
	b.decoders[event.ID] = &displayDecoder{}
	b.append("system", b.spacing.Before(2)+fmt.Sprintf("s%d %s $ ", b.next, event.StartedAt.Format("15:04:05")))
	b.append("command", event.Command+"\n")
	b.last = event.ID
}

func (b *logBuffer) switchSource(id string) {
	if b.last != id {
		if n, ok := b.shells[id]; ok {
			b.append("system", b.spacing.Before(2)+fmt.Sprintf("s%d\n", n))
		}
		b.last = id
	}
}

func (b *logBuffer) output(id string, data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	decoder := b.decoders[id]
	if decoder == nil {
		return
	}
	text := decoder.output.Text(decoder.text(string(data)))
	if b.last != id {
		text = logview.TrimLeadingBlankLines(text)
	}
	if text == "" {
		return
	}
	b.switchSource(id)
	b.append("output", text)
}

func (b *logBuffer) clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = ""
	b.spans = nil
	b.last = ""
	b.spacing = logview.Spacing{}
	for _, decoder := range b.decoders {
		decoder.output = logview.Output{}
	}
}

// The GUI is a log viewer, not a terminal emulator. Escape state spans chunks.
type displayDecoder struct {
	state   byte
	pending string
	output  logview.Output
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
