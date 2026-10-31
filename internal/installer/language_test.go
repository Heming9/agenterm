package installer

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestShellAuthorizationLanguage(t *testing.T) {
	for _, tc := range []struct{ locale, want string }{
		{"zh-CN", "是否允许连接？"}, {"en", "Allow connection?"}, {"ja-JP", "Allow connection?"}, {"zh-TW", "Allow connection?"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			cmd := exec.Command("sh", "install.sh", "example", "https://example.test/downloads/v1")
			cmd.Env = append(os.Environ(), "AGENTERM_LANG="+tc.locale)
			cmd.Stdin = strings.NewReader("n\n")
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("output %q, error %v", out, err)
			}
		})
	}
}
