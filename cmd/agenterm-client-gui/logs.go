package main

import (
	"agenterm/internal/client"
	"agenterm/internal/logview"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

const maxLogBytes = 10 * 1024 * 1024

// Each connection owns a buffer so a finishing old process cannot write into a
// newly connected session. Pending and displayed output are both bounded.
type logBuffer struct {
	mu       sync.Mutex
	size     int
	spans    []logSpan
	last     string
	decoders map[string]*logview.Decoder
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
	b.size += len(text)
	if len(b.spans) > 0 && b.spans[len(b.spans)-1].kind == kind {
		b.spans[len(b.spans)-1].text += text
	} else {
		b.spans = append(b.spans, logSpan{kind, text})
	}
	b.spans, b.size = trimLogSpans(b.spans, b.size)
}

// Both pending output and displayed history use the same UTF-8-safe limit.
func trimLogSpans(spans []logSpan, total int) ([]logSpan, int) {
	for total > maxLogBytes && len(spans) > 0 {
		excess := total - maxLogBytes
		if len(spans[0].text) <= excess {
			total -= len(spans[0].text)
			spans[0] = logSpan{}
			spans = spans[1:]
			continue
		}
		old := spans[0].text
		tail := old[excess:]
		for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
			tail = tail[1:]
		}
		// Release the discarded prefix's backing allocation as well.
		spans[0].text = strings.Clone(tail)
		total -= len(old) - len(tail)
	}
	return spans, total
}

var _ io.Writer = (*logBuffer)(nil)

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.append("output", string(p))
	return len(p), nil
}

func (b *logBuffer) takeSpans() []logSpan {
	b.mu.Lock()
	defer b.mu.Unlock()
	spans := b.spans
	b.size = 0
	b.spans = nil
	return spans
}

// command and output serialize log entries, including concurrent output.
func (b *logBuffer) command(event client.CommandEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if event.Done {
		if event.Error != "" {
			b.switchSource(event.ID)
			b.append("error", b.spacing.Before(1)+event.Error+"\n")
		}
		delete(b.decoders, event.ID)
		return
	}
	if b.decoders == nil {
		b.decoders = make(map[string]*logview.Decoder)
	}
	b.decoders[event.ID] = &logview.Decoder{}
	b.append("system", b.spacing.Before(2)+event.StartedAt.Format("15:04:05")+" $ ")
	b.append("command", event.Command+"\n")
	b.last = event.ID
}

func (b *logBuffer) switchSource(id string) {
	if b.last != id {
		if b.decoders[id] != nil {
			b.append("system", b.spacing.Before(2))
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
	text := decoder.Output.Text(decoder.Text(string(data)))
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
	b.size = 0
	b.spans = nil
	b.last = ""
	b.spacing = logview.Spacing{}
	for _, decoder := range b.decoders {
		decoder.Output = logview.Output{}
	}
}
