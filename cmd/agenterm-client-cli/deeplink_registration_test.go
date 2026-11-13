package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyCLILinkExecutableSurvivesSourceRemoval(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "temporary-client")
	if err := os.WriteFile(source, []byte("client binary"), 0700); err != nil {
		t.Fatal(err)
	}
	dest, err := copyCLILinkExecutable(source, filepath.Join(dir, "installed"))
	if err != nil {
		t.Fatal(err)
	}
	if again, err := copyCLILinkExecutable(source, filepath.Join(dir, "installed")); err != nil || again != dest {
		t.Fatalf("reuse = %q, %v", again, err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "client binary" {
		t.Fatalf("installed copy = %q, %v", data, err)
	}
}

func TestCLILinkLaunchDetection(t *testing.T) {
	for _, tc := range []struct {
		args []string
		link bool
	}{
		{nil, false}, {[]string{"-token", "test"}, false},
		{[]string{"agenterm-cli://connect?url=test"}, true}, {[]string{"-link-file", "path"}, true},
	} {
		if got := isCLILinkLaunch(tc.args); got != tc.link {
			t.Errorf("isCLILinkLaunch(%v) = %v", tc.args, got)
		}
	}
}
