package client

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSessionListCommandPreviewPreservesExecution(t *testing.T) {
	m := testManager(t)
	command := "echo " + strings.Repeat("中🙂", 120)
	out := runAPI(t, m, ExecuteRequest{Setup: &Setup{SessionID: "preview"}, Command: command})
	if !strings.Contains(out.Output, strings.Repeat("中🙂", 120)) {
		t.Fatal("command was shortened before execution")
	}
	list, err := m.List(context.Background())
	if err != nil || len(list.Sessions) != 1 {
		t.Fatal(list, err)
	}
	info := list.Sessions[0]
	if !strings.HasSuffix(info.Command, "... (truncated)") || !utf8.ValidString(info.Command) || utf8.RuneCountInString(info.Command) != 160 || !strings.HasPrefix(command, strings.TrimSuffix(info.Command, "... (truncated)")) {
		t.Fatalf("invalid command preview: %+v", info)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9]{8}$`).MatchString(out.CommandID) || info.CommandID != out.CommandID {
		t.Fatal("execution and list command IDs differ or are not eight alphanumeric characters")
	}
	// Reuse updates one list entry; the old pinned command must be rejected.
	next := runAPI(t, m, ExecuteRequest{SessionID: "preview", Command: "echo next"})
	if next.CommandID == out.CommandID {
		t.Fatal("replacement reused the previous command ID")
	}
	if _, err := m.Read(context.Background(), ReadRequest{SessionID: "preview", CommandID: out.CommandID}); err == nil {
		t.Fatal("stale pinned read accepted")
	}
	list, err = m.List(context.Background())
	if err != nil || len(list.Sessions) != 1 || list.Sessions[0].Command != "echo next" {
		t.Fatal("reuse did not replace the command preview", list, err)
	}
}

func TestCommandPreviewBoundaryAndRepeatedSummaries(t *testing.T) {
	for _, command := range []string{strings.Repeat("中🙂", 80), strings.Repeat("中🙂", 81)} {
		info := SessionInfo{Command: command}
		info.Summarize()
		preview := info.Command
		info.Summarize()
		if info.Command != preview || utf8.RuneCountInString(preview) != 160 || !utf8.ValidString(preview) {
			t.Fatal("repeated summarization changed the preview", preview)
		}
		if utf8.RuneCountInString(command) == 160 && preview != command {
			t.Fatal("command within limit was shortened")
		}
		if utf8.RuneCountInString(command) > 160 && !strings.HasSuffix(preview, "... (truncated)") {
			t.Fatal("shortened command is missing its suffix")
		}
	}
}

func TestShortCommandIDCollisionAndEntropyFailure(t *testing.T) {
	m := &Manager{history: map[string]*record{"retained": {commandID: "00000000"}}}
	// The first eight random bytes collide. The generator must try again.
	source := bytes.NewReader(append(make([]byte, 8), bytes.Repeat([]byte{1}, 8)...))
	id, err := m.commandIDLocked(source)
	if err != nil || id != "11111111" {
		t.Fatal(id, err)
	}
	if _, err := m.commandIDLocked(bytes.NewReader(nil)); err != io.EOF {
		t.Fatalf("entropy failure was not propagated: %v", err)
	}
}
