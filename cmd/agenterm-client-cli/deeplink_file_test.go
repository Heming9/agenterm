//go:build !windows

package main

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestConsumeLinkFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agenterm-link.12345")
	link := "agenterm-cli://connect?url=" + url.QueryEscape("https://example.com/c/ab1234")
	if err := os.WriteFile(path, []byte(link), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := consumeLinkFile(path, dir); err != nil || got != link {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("link file not removed")
	}
	modeLink := "agenterm-cli://connect?" + url.Values{"url": {"https://example.com/c/ab1234"}, "mode": {"persistent"}}.Encode()
	if err := os.WriteFile(path, []byte(modeLink), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := consumeLinkFile(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	args, err := deepLinkArgs([]string{raw})
	if err != nil || len(args) != 4 || args[3] != "persistent" {
		t.Fatalf("file mode: %v %v", args, err)
	}
	if err := os.WriteFile(path, []byte(link), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := consumeLinkFile(path, dir); err == nil {
		t.Fatal("accepted readable link file")
	}
}
