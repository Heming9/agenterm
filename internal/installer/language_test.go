package installer

import (
	"agenterm/internal/i18n"
	"agenterm/internal/theme"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestShellInstallerLanguage(t *testing.T) {
	for _, tc := range []struct{ locale, want string }{
		{"zh-CN", "准备客户端"}, {"en", "Preparing client"}, {"ja-JP", "Preparing client"}, {"zh-TW", "Preparing client"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			script, err := Script("install.sh")
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "install.sh")
			if err := os.WriteFile(path, script, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "curl"), []byte("#!/bin/sh\nexit 17\n"), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", path, "example", "https://example.test/downloads/v1")
			cmd.Env = append(os.Environ(), "AGENTERM_LANG="+tc.locale, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+dir, "XDG_CACHE_HOME="+dir)
			out, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 17 || !strings.Contains(string(out), tc.want) {
				t.Fatalf("output %q, error %v", out, err)
			}
			logo := theme.TerminalLogo()
			wordmark := theme.InstallerWordmark()
			if strings.Contains(string(out), "╭──────────────────────────────────────────────────────╮") ||
				!strings.Contains(string(out), "  "+logo[0]+"  "+wordmark[0]) ||
				!strings.Contains(string(out), "  "+logo[1]+"   "+wordmark[1]) ||
				!strings.Contains(string(out), "  "+logo[2]+"  Agenterm") {
				t.Fatalf("missing compact banner or terminal logo: %s", out)
			}
			if strings.Contains(string(out), "Choose authorization") || strings.Contains(string(out), "选择授权方式") {
				t.Fatalf("installer prompted for authorization: %s", out)
			}
		})
	}
}

func TestInstallerResourceReferences(t *testing.T) {
	for _, name := range []string{"install.sh", "install.ps1"} {
		template, err := content.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range regexp.MustCompile(`installer\.[a-z0-9.]+`).FindAllString(string(template), -1) {
			if i18n.Text("en", key) == key {
				t.Errorf("%s: missing resource %s", name, key)
			}
		}
		script, err := Script(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(script), "@TRANSLATIONS@") {
			t.Errorf("%s: unresolved translation marker", name)
		}
		if strings.Contains(string(script), "@LOGO_") || strings.Contains(string(script), "@WORDMARK_") || strings.Contains(string(script), "@COLOR_") {
			t.Errorf("%s: unresolved theme marker", name)
		}
	}
}
