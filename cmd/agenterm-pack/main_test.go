package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFailedPackPreservesExistingBundle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "components.tar.xz")
	previous := []byte("previous valid release")
	if err := os.WriteFile(path, previous, 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeBundle(path, map[string][]byte{"invalid": []byte("broken")}, "0.1.0"); err == nil {
		t.Fatal("invalid component set was accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(previous) {
		t.Fatalf("previous release changed: %q, %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary archive was not cleaned up: %v, %v", entries, err)
	}
}
