package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLogBufferTrimsAcrossSpansAndResets(t *testing.T) {
	var buffer logBuffer
	buffer.append("system", "old\n")
	buffer.append("output", strings.Repeat("中", maxLogBytes/3))
	buffer.append("error", "failure\n")
	text := buffer.take()
	if len(text) > maxLogBytes || !utf8.ValidString(text) || !strings.HasSuffix(text, "failure\n") || strings.HasPrefix(text, "old\n") {
		t.Fatal("history truncation lost the tail or split a rune")
	}
	buffer.append("output", "after drain")
	if got := buffer.take(); got != "after drain" {
		t.Fatalf("drained bytes counted against new output: %q", got)
	}
	buffer.append("output", strings.Repeat("x", maxLogBytes))
	buffer.clear()
	buffer.append("output", "after clear")
	if got := buffer.take(); got != "after clear" {
		t.Fatalf("cleared bytes counted against new output: %q", got)
	}
}

func BenchmarkLogBufferBurst(b *testing.B) {
	text := strings.Repeat("x", 128)
	b.ReportAllocs()
	for b.Loop() {
		var buffer logBuffer
		for i := range 256 {
			kind := "output"
			if i%2 == 0 {
				kind = "system"
			}
			buffer.append(kind, text)
		}
		buffer.takeSpans()
	}
}
