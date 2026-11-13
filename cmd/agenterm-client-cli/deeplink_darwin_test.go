package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMacLinkScriptCompiles(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "Agenterm CLI.app")
	script := macLinkScript("/tmp/Agenterm's CLI", "/tmp/Agenterm Inbox")
	if out, err := exec.Command("/usr/bin/osacompile", "-o", bundle, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("compile AppleScript: %v: %s", err, out)
	}
	if err := configureMacLinkBundle(bundle); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", bundle).CombinedOutput(); err != nil {
		t.Fatalf("verify link handler signature: %v: %s", err, out)
	}
}
