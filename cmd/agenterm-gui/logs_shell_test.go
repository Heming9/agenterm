package main

import (
	"strings"
	"testing"
	"time"

	"agenterm/internal/client"
)

func TestShellLogAttribution(t *testing.T) {
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
	for _, want := range []string{"s1  12:00:00  $ first\nfirst output", "s2  12:00:00  $ second\nsecond output\ncontinued", "\ns1\nback\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	b.clear()
	b.output("one", []byte("after clear\n"))
	if got := b.take(); got != "\ns1\nafter clear\n" {
		t.Fatalf("output after clear: %q", got)
	}
	b.command(client.CommandEvent{ID: "one", Done: true})
	b.command(client.CommandEvent{ID: "three", Command: "third", StartedAt: started})
	if got := b.take(); !strings.Contains(got, "s3  12:00:00  $ third") {
		t.Fatalf("new shell number: %q", got)
	}
}
