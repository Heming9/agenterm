package service

import (
	"context"
	"testing"
	"time"
)

func assertCompactReadResult(t *testing.T, r map[string]any) {
	t.Helper()
	core := []string{"session_id", "command_id", "status", "output", "next_cursor", "has_more"}
	allowed := make(map[string]bool)
	for _, field := range core {
		allowed[field] = true
		if _, ok := r[field]; !ok {
			t.Fatalf("missing %s: %v", field, r)
		}
	}
	for _, field := range []string{"exit_code", "error", "truncated"} {
		allowed[field] = true
	}
	for field := range r {
		if !allowed[field] {
			t.Fatalf("unexpected result field %s: %v", field, r)
		}
	}
	if value, ok := r["truncated"]; ok && value != true {
		t.Fatalf("redundant truncation flag: %v", r)
	}
	switch r["status"] {
	case "running":
	case "idle", "closed":
		if _, ok := r["exit_code"]; !ok {
			t.Fatalf("finished command has no exit code: %v", r)
		}
	default:
		t.Fatalf("unknown state: %v", r)
	}
}

func TestCompactReadLifecycleStates(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		done, commandDone, retained, stopping bool
		want                                  string
	}{
		{name: "active", retained: true, want: "running"},
		{name: "reusable", commandDone: true, retained: true, want: "idle"},
		{name: "anonymous-closing", commandDone: true, want: "running"},
		{name: "stop-pending", commandDone: true, retained: true, stopping: true, want: "running"},
		{name: "exited", done: true, commandDone: true, want: "closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &session{id: "test", commandID: "command", done: tc.done, commandDone: tc.commandDone, keepAlive: tc.retained, stopRequested: tc.stopping, changed: make(chan struct{})}
			r, err := p.readCommand(context.Background(), 0, 0, defaultSessionReadBytes, "command")
			if err != nil {
				t.Fatal(err)
			}
			assertCompactReadResult(t, r)
			if r["status"] != tc.want {
				t.Fatal(r)
			}
			wantFields := 6
			if tc.done || tc.commandDone {
				wantFields++
				if r["exit_code"] != 0 {
					t.Fatal(r)
				}
			}
			if len(r) != wantFields {
				t.Fatalf("redundant fields: %v", r)
			}
			p.owner = "owner"
			p.client = &client{info: clientInfo{ID: "device"}}
			p.ended = time.Now()
			state := &runtimeState{sessions: map[string]*session{sessionKey(p.owner, p.id): p}}
			listed := state.listSessions(p.owner, "")
			if len(listed) != 1 || listed[0].Status != r["status"] {
				t.Fatalf("list/read state mismatch: %v %v", listed, r)
			}

		})
	}
}
