package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareMacLinkBundle(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, source := range []string{executable, filepath.Join(dir, "Source.app", "Contents", "MacOS", "Agenterm")} {
		if source != executable {
			if err := os.MkdirAll(filepath.Dir(source), 0755); err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(executable)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, contents, 0755); err != nil {
				t.Fatal(err)
			}
		}
		bundle, err := prepareMacLinkBundle(source, dir)
		if err != nil {
			t.Fatal(err)
		}
		plist, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(plist), "CFBundleURLSchemes") || !strings.Contains(string(plist), "<string>agenterm</string>") {
			t.Fatal("missing URL scheme")
		}
		if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", bundle).CombinedOutput(); err != nil {
			t.Fatalf("verify bundle: %v: %s", err, out)
		}
	}
}
