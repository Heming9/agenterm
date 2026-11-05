//go:build !windows

package installer

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestInteractiveConsentDefaultAndDenial(t *testing.T) {
	for _, tc := range []struct {
		name, keys, want string
		width            uint16
		verticalRedraw   bool
	}{
		{"default temporary", "\r", "连接进度", 80, false},
		{"deny narrow", "\x1b[C\x1b[C\r", "已取消连接。", 80, true},
		{"deny wide", "\x1b[C\x1b[C\r", "已取消连接。", 207, false},
		{"deny with vertical arrows", "\x1b[B\x1b[A\x1b[B\x1b[B\r", "已取消连接。", 80, true},
		{"return with up arrow", "\x1b[B\x1b[B\x1b[A\x1b[A\r", "连接进度", 80, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script, err := Script("install.sh")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "install.sh")
			if err := os.WriteFile(path, script, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", path, "preview", "http://127.0.0.1:1/downloads/v1")
			cmd.Env = append(os.Environ(), "AGENTERM_LANG=zh-CN", "TERM=xterm")
			terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: tc.width})
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			output := make(chan []byte, 32)
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := terminal.Read(buf)
					if n > 0 {
						output <- append([]byte(nil), buf[:n]...)
					}
					if err != nil {
						close(output)
						return
					}
				}
			}()
			var text bytes.Buffer
			timeout := time.NewTimer(10 * time.Second)
			defer timeout.Stop()
			sent := false
			for {
				select {
				case data, ok := <-output:
					if !ok {
						if !strings.Contains(text.String(), tc.want) {
							t.Fatalf("missing %q in %q", tc.want, text.String())
						}
						_ = cmd.Wait()
						return
					}
					text.Write(data)
					if !sent && strings.Contains(text.String(), "▶ 允许授权（临时连接）") {
						if _, err := io.WriteString(terminal, tc.keys); err != nil {
							t.Fatal(err)
						}
						sent = true
					}
					if sent && strings.Contains(text.String(), tc.want) {
						if got := strings.Contains(text.String(), "\x1b[2A"); got != tc.verticalRedraw {
							t.Fatalf("vertical redraw = %v, want %v: %q", got, tc.verticalRedraw, text.String())
						}
						return
					}
				case <-timeout.C:
					t.Fatalf("timed out: %s", text.String())
				}
			}
		})
	}
}
