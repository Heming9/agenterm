package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"agenterm/internal/protocol"
)

func TestSessionIncrementalReadAndExit(t *testing.T) {
	s := &session{id: "test", changed: make(chan struct{})}
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.accept(protocol.Message{Type: "output", Output: "prompt> "})
	}()
	first, err := s.read(context.Background(), 0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if first["output"] != "prompt> " || first["done"] != false {
		t.Fatalf("unexpected first read: %#v", first)
	}
	cursor := first["next_cursor"].(int64)
	s.accept(protocol.Message{Type: "output", Output: "answer"})
	s.accept(protocol.Message{Type: "done", ExitCode: 7})
	last, err := s.read(context.Background(), cursor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if last["output"] != "answer" || last["done"] != true || last["exit_code"] != 7 {
		t.Fatalf("unexpected final read: %#v", last)
	}
}
func TestSessionBufferReportsTruncation(t *testing.T) {
	s := &session{id: "test", changed: make(chan struct{})}
	s.accept(protocol.Message{Type: "output", Output: strings.Repeat("x", maxSessionOutput+10)})
	r, err := s.read(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r["truncated"] != true || len(r["output"].(string)) != maxSessionOutput || r["next_cursor"] != int64(maxSessionOutput+10) {
		t.Fatalf("bad retained output metadata")
	}
}
