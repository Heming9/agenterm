package terminalhistory

import (
	"agenterm/internal/protocol"
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSessionIncrementalReadAndExit(t *testing.T) {
	s := &Record{id: "test", changed: make(chan struct{})}
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.Accept(protocol.Message{Type: "output", Output: "prompt> "})
	}()
	first, err := s.readCommand(context.Background(), 0, time.Second, defaultSessionReadBytes, "")
	if err != nil {
		t.Fatal(err)
	}
	if first["output"] != "prompt> " || first["status"] == "closed" {
		t.Fatalf("unexpected first read: %#v", first)
	}
	cursor := first["next_cursor"].(int64)
	s.Accept(protocol.Message{Type: "output", Output: "answer"})
	s.Accept(protocol.Message{Type: "done", ExitCode: 7})
	last, err := s.readCommand(context.Background(), cursor, 0, defaultSessionReadBytes, "")
	if err != nil {
		t.Fatal(err)
	}
	if last["output"] != "answer" || last["status"] != "closed" || last["exit_code"] != 7 {
		t.Fatalf("unexpected final read: %#v", last)
	}
}
func TestSessionBufferReportsTruncation(t *testing.T) {
	s := &Record{id: "test", keepAlive: true, changed: make(chan struct{})}
	s.Accept(protocol.Message{Type: "output", Output: strings.Repeat("x", maxSessionOutput+10)})
	r, err := s.readCommand(context.Background(), 0, 0, maxSessionReadBytes, "")
	if err != nil {
		t.Fatal(err)
	}
	if r["truncated"] != true || len(r["output"].(string)) != maxSessionReadBytes || r["next_cursor"] != int64(maxSessionReadBytes+10) || r["has_more"] != true {
		t.Fatalf("bad retained output metadata")
	}
	retained := len(r["output"].(string))
	for r["has_more"] == true {
		r, err = s.readCommand(context.Background(), r["next_cursor"].(int64), 0, maxSessionReadBytes, "")
		if err != nil {
			t.Fatal(err)
		}
		retained += len(r["output"].(string))
	}
	if retained != maxSessionOutput || r["next_cursor"] != int64(maxSessionOutput+10) {
		t.Fatal("lost retained output across pages")
	}
}

func TestAnonymousOutputIsBoundedAfterRetention(t *testing.T) {
	s := &Record{id: "test", changed: make(chan struct{})}
	s.Accept(protocol.Message{Type: "output", Output: strings.Repeat("x", maxSessionOutput+10)})
	if len(s.output) != maxSessionOutput+10 {
		t.Fatal("anonymous output was trimmed before retention")
	}
	s.Accept(protocol.Message{Type: "session_retained", SessionPersistent: true})
	if len(s.output) != maxSessionOutput || s.offset != 10 {
		t.Fatalf("retained output not bounded: size=%d offset=%d", len(s.output), s.offset)
	}
}

func TestSessionReadUTF8PaginationAfterExit(t *testing.T) {
	p := &Record{id: "utf8", changed: make(chan struct{})}
	want := "abc你好🙂xyz"
	p.Accept(protocol.Message{Type: "output", Output: want})
	p.Accept(protocol.Message{Type: "done", ExitCode: 7})
	var got strings.Builder
	var cursor int64
	for {
		r, err := p.readCommand(context.Background(), cursor, 0, 4, "")
		if err != nil {
			t.Fatal(err)
		}
		output := r["output"].(string)
		if len(output) > 4 || !utf8.ValidString(output) || r["status"] != "closed" || r["exit_code"] != 7 || r["truncated"] != nil {
			t.Fatalf("invalid page: %v", r)
		}
		next := r["next_cursor"].(int64)
		if next != cursor+int64(len(output)) || next <= cursor {
			t.Fatalf("cursor did not advance by returned bytes: %v", r)
		}
		got.WriteString(output)
		cursor = next
		if r["has_more"] == false {
			break
		}
	}
	if got.String() != want || cursor != int64(len(want)) {
		t.Fatalf("lost output: %q", got.String())
	}
	if _, err := p.readCommand(context.Background(), 4, 0, 4, ""); err == nil {
		t.Fatal("accepted a cursor inside a multibyte character")
	}
	for _, limit := range []int{0, 1, 3, maxSessionReadBytes + 1} {
		if _, err := p.readCommand(context.Background(), 0, 0, limit, ""); err == nil {
			t.Fatalf("accepted invalid limit %d", limit)
		}
	}
	if _, err := p.readCommand(context.Background(), -1, 0, 4, ""); err == nil {
		t.Fatal("accepted negative cursor")
	}
	if _, err := p.readCommand(context.Background(), cursor+1, 0, 4, ""); err == nil {
		t.Fatal("accepted cursor beyond output")
	}
}

func TestSessionTruncationPreservesUTF8Boundary(t *testing.T) {
	p := &Record{id: "utf8", keepAlive: true, changed: make(chan struct{})}
	p.Accept(protocol.Message{Type: "output", Output: strings.Repeat("你", maxSessionOutput/3+2)})
	r, err := p.readCommand(context.Background(), 0, 0, 4, "")
	if err != nil || r["truncated"] != true || r["output"] != "你" || !utf8.Valid(p.output) || p.offset%3 != 0 {
		t.Fatalf("invalid truncated page: %v err=%v offset=%d", r, err, p.offset)
	}
}

func TestAnonymousExecutionIDDependsOnRemainingOutput(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		code         int
		wantID, more bool
	}{
		{name: "empty"},
		{name: "small", output: "done"},
		{name: "failed", output: "failure", code: 7},
		{name: "exact-limit", output: strings.Repeat("x", defaultSessionReadBytes)},
		{name: "over-limit", output: strings.Repeat("x", defaultSessionReadBytes+1), wantID: true, more: true},
		{name: "utf8-boundary", output: strings.Repeat("你", defaultSessionReadBytes/3+1), wantID: true, more: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := New("anonymous")
			record.Begin("command", "test", false)
			record.Accept(protocol.Message{Type: "output", Output: tc.output})
			record.Accept(protocol.Message{Type: "done", ExitCode: tc.code})
			result, err := record.Wait(context.Background(), protocol.SessionReadRequest{CommandID: "command", AwaitSettlement: true, WaitMS: 10000})
			if err != nil {
				t.Fatal(err)
			}
			_, hasID := result["session_id"]
			if hasID != tc.wantID || result["has_more"] != tc.more || result["status"] != "closed" || result["exit_code"] != tc.code || result["truncated"] != nil {
				t.Fatalf("unexpected result metadata: id=%v more=%v status=%v exit=%v truncated=%v", result["session_id"], result["has_more"], result["status"], result["exit_code"], result["truncated"])
			}
			output := result["output"].(string)
			if len(output) > defaultSessionReadBytes || !utf8.ValidString(output) {
				t.Fatal("invalid first page")
			}
			for result["has_more"] == true {
				result, err = record.Read(context.Background(), protocol.SessionReadRequest{CommandID: "command", Cursor: result["next_cursor"].(int64)})
				if err != nil {
					t.Fatal(err)
				}
				output += result["output"].(string)
			}
			if output != tc.output {
				t.Fatal("lost output across pages")
			}
		})
	}
}

func TestAnonymousExecutionKeepsIDForTruncatedHistory(t *testing.T) {
	record := New("anonymous")
	record.Begin("command", "test", false)
	record.Accept(protocol.Message{Type: "output", Output: "tail"})
	record.offset = 10 // A caller requests bytes older than the available history.
	record.Accept(protocol.Message{Type: "done"})
	result, err := record.Wait(context.Background(), protocol.SessionReadRequest{CommandID: "command", AwaitSettlement: true})
	if err != nil || result["session_id"] != "anonymous" || result["truncated"] != true || result["has_more"] != false {
		t.Fatalf("truncated history lost its ID: %v err=%v", result, err)
	}
}
