package p2p

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManifestRejectsTamperingAndScriptsKeepCredentialsScoped(t *testing.T) {
	root := t.TempDir()
	name, _ := Filename("linux", "amd64")
	payload := []byte("archive")
	hash := sha256.Sum256(payload)
	manifest := Manifest{Version: "0.1.0", Artifacts: []Artifact{{Platform: "linux", Arch: "amd64", File: name, SHA256: hex.EncodeToString(hash[:])}}}
	encoded, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(root, "manifest.json"), encoded, 0600)
	os.WriteFile(filepath.Join(root, name), payload, 0600)
	if _, err := Load(root); err != nil {
		t.Fatal(err)
	}
	config := []byte(`{"session":"0123456789abcdef0123456789abcdef","token":"private-test-token"}`)
	sh, ps := Scripts(manifest, "https://example.test/custom", config, false, false)
	if strings.Contains(sh, "@") || strings.Contains(ps, "@") || !strings.Contains(sh, "https://example.test/custom/p2p/downloads/") {
		t.Fatal("bootstrap placeholders or prefix incorrect")
	}
	stopSh, stopPS := Scripts(manifest, "https://example.test/custom", config, false, true)
	if strings.Contains(stopSh, "private-test-token") || strings.Contains(stopPS, "private-test-token") || !strings.Contains(stopSh, "--session '0123456789abcdef0123456789abcdef'") {
		t.Fatal("stop script must be scoped without an access token")
	}
	if runtime.GOOS != "windows" {
		for _, script := range []string{sh, stopSh} {
			command := exec.Command("/bin/sh", "-n")
			command.Stdin = strings.NewReader(script)
			if out, err := command.CombinedOutput(); err != nil {
				t.Fatalf("shell syntax: %v %s", err, out)
			}
		}
	} else {
		for i, script := range []string{ps, stopPS} {
			path := filepath.Join(root, fmt.Sprintf("bootstrap-%d.ps1", i))
			if err := os.WriteFile(path, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `$tokens=$null;$errors=$null;[System.Management.Automation.Language.Parser]::ParseFile($env:P2P_PARSE_FILE,[ref]$tokens,[ref]$errors)|Out-Null;if($errors.Count){$errors|Out-String|Write-Error;exit 1}`)
			command.Env = append(os.Environ(), "P2P_PARSE_FILE="+path)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("PowerShell syntax: %v %s", err, output)
			}
		}
	}
	os.WriteFile(filepath.Join(root, name), []byte("changed"), 0600)
	if _, err := Load(root); err == nil {
		t.Fatal("tampered component accepted")
	}
	manifest.Artifacts[0].File = "../outside"
	encoded, _ = json.Marshal(manifest)
	os.WriteFile(filepath.Join(root, "manifest.json"), encoded, 0600)
	if _, err := Load(root); err == nil {
		t.Fatal("path traversal accepted")
	}
}
