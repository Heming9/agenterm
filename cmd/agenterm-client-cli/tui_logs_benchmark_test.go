package main

import (
	"fmt"
	"strings"
	"testing"

	"agenterm/internal/client"
	"agenterm/internal/logview"
)

func benchmarkLogHistory(b *testing.B, blocks int) *tuiModel {
	b.Helper()
	m := newTUI("")
	b.Cleanup(m.cancel)
	m.legacyCells = false
	m.viewport.Width, m.viewport.Height = 78, 20
	text := strings.Repeat("历史输出 with emoji 👩‍💻 and a long line that wraps in the terminal.\n", 32)
	for range blocks {
		m.spans = append(m.spans, logSpan{kind: "output", text: text})
		m.logBytes += len(text)
	}
	m.spans = append(m.spans, logSpan{kind: "system", text: "next command\n"})
	m.logBytes += len("next command\n")
	m.refreshLog()
	return m
}

func BenchmarkTUIAppendWithHistory(b *testing.B) {
	for _, blocks := range []int{0, 16, 64} {
		b.Run(fmt.Sprintf("blocks=%d", blocks), func(b *testing.B) {
			m := benchmarkLogHistory(b, blocks)
			count, size := len(m.spans), m.logBytes
			b.ReportAllocs()
			for b.Loop() {
				m.spans, m.logBytes = m.spans[:count], size
				m.appendLog("output", "new output\n")
			}
		})
	}
}

func BenchmarkTUIOutputBurst(b *testing.B) {
	m := benchmarkLogHistory(b, 16)
	count, size := len(m.spans), m.logBytes
	output := []byte("new output 中🙂\n")
	b.ReportAllocs()
	for b.Loop() {
		m.spans, m.logBytes = m.spans[:count], size
		m.decoders["live"] = &logview.Decoder{}
		for range 64 {
			m.events <- tuiEvent{id: "live", output: output}
		}
		m.events <- tuiEvent{command: &client.CommandEvent{ID: "live", Done: true}}
		for len(m.events) > 0 {
			m.Update(m.waitEvent()())
		}
	}
}
