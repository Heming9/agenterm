package service

import (
	"testing"
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
