package p2p

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestP2PBootstrapPowerShellPreservesNativeJSONInput(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows PowerShell and a Windows executable")
	}
	root := t.TempDir()
	source := filepath.Join(root, "main.go")
	if err := os.WriteFile(source, []byte(`package main
import ("encoding/json"; "fmt"; "io"; "os")
func main() {
    data, err := io.ReadAll(os.Stdin)
    if err != nil || !json.Valid(data) { fmt.Fprintln(os.Stderr, "invalid UTF-8 JSON input"); os.Exit(1) }
    foreground := len(os.Args) == 5 && os.Args[4] == "--foreground"
    json.NewEncoder(os.Stdout).Encode(map[string]any{"config": json.RawMessage(data), "foreground": foreground})
}
`), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "fixture.exe")
	if output, err := exec.Command("go", "build", "-o", binary, source).CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v %s", err, output)
	}
	payload, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(payload)
	expected := hex.EncodeToString(hash[:])
	name, err := Filename(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: "test-1", Artifacts: []Artifact{{Platform: runtime.GOOS, Arch: runtime.GOARCH, File: name, SHA256: expected}}}
	cache := filepath.Join(root, "agenterm", "p2p-cli", "test-1-"+expected)
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "p2p-cli.exe"), payload, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", root)
	config := []byte(`{"session":"0123456789abcdef0123456789abcdef","token":"private-test-token","label":"配置输入"}`)
	for _, foreground := range []bool{false, true} {
		t.Run(fmt.Sprintf("foreground=%v", foreground), func(t *testing.T) {
			embedded := config
			t.Setenv("AGENTERM_P2P_CONFIG_B64", "")
			if foreground {
				embedded = nil
				t.Setenv("AGENTERM_P2P_CONFIG_B64", base64.StdEncoding.EncodeToString(config))
			}
			_, script := Scripts(manifest, "https://example.test", embedded, foreground, false)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("bootstrap: %v %s", err, output)
			}
			var result struct {
				Config     map[string]string `json:"config"`
				Foreground bool              `json:"foreground"`
			}
			if err := json.Unmarshal(output, &result); err != nil || result.Config["label"] != "配置输入" || result.Foreground != foreground {
				t.Fatalf("native input changed: %s (%v)", output, err)
			}
		})
	}
}
