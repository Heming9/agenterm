package main

import (
	"strings"
	"testing"
	"time"

	"agenterm/internal/client"
)

func TestCommandLogInterleavedOutput(t *testing.T) {
	var b logBuffer
	started := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	b.command(client.CommandEvent{ID: "one", Command: "first", StartedAt: started})
	b.output("one", []byte("first output\n"))
	b.command(client.CommandEvent{ID: "two", Command: "second", StartedAt: started})
	b.output("two", []byte("second output\n"))
	b.output("one", []byte("\x1b[31"))
	b.output("two", []byte("continued\n"))
	b.output("one", []byte("mback\x1b[0m\n"))
	got := b.take()
	want := "12:00:00 $ first\nfirst output\n\n12:00:00 $ second\nsecond output\ncontinued\n\nback\n"
	if got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
	b.clear()
	b.output("one", []byte("after clear\n"))
	if got := b.take(); got != "after clear\n" {
		t.Fatalf("output after clear: %q", got)
	}
	b.command(client.CommandEvent{ID: "one", Done: true})
	b.command(client.CommandEvent{ID: "three", Command: "third", StartedAt: started})
	if got, want := b.take(), "\n12:00:00 $ third\n"; got != want {
		t.Fatalf("new command = %q, want %q", got, want)
	}
}

func TestShellLogSpacingAcrossFlushes(t *testing.T) {
	var b logBuffer
	started := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	b.command(client.CommandEvent{ID: "one", Command: "pwd", StartedAt: started})
	got := b.take()
	for _, chunk := range []string{"\r\n", " \r\nPath\r", "\n----\r\nC:\\Users\\dmhe", "\r\n\r\n", "\r\n"} {
		b.output("one", []byte(chunk))
		// Draining a GUI frame must not lose the displayed tail's spacing state.
		for _, span := range b.takeSpans() {
			got += span.text
		}
	}
	b.command(client.CommandEvent{ID: "one", Done: true})
	b.command(client.CommandEvent{ID: "two", Command: "printf test", StartedAt: started})
	b.output("two", []byte("test")) // No newline from the process.
	got += b.take()
	b.command(client.CommandEvent{ID: "three", Command: "next", StartedAt: started})
	got += b.take()
	want := "12:00:00 $ pwd\nPath\n----\nC:\\Users\\dmhe\n\n12:00:00 $ printf test\ntest\n\n12:00:00 $ next\n"
	if got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestShellLogSpacingWhenOutputResumes(t *testing.T) {
	var b logBuffer
	b.command(client.CommandEvent{ID: "one", Command: "first"})
	b.output("one", []byte("first\n \n\n"))
	b.command(client.CommandEvent{ID: "two", Command: "second"})
	b.output("two", []byte("second"))
	b.take()
	b.output("one", []byte("  back\n\ninside\n\n"))
	if got, want := b.take(), "\n\n  back\n\ninside\n"; got != want {
		t.Fatalf("resumed output = %q, want %q", got, want)
	}
}

// Tests inspect plain text through the same span drain used by the GUI.
func (b *logBuffer) take() string {
	var text strings.Builder
	for _, span := range b.takeSpans() {
		text.WriteString(span.text)
	}
	return text.String()
}
