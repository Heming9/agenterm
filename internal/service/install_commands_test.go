package service

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agenterm/internal/protocol"
)

func TestInstallCommandsFollowPublicURLProtocol(t *testing.T) {
	for _, tls := range []bool{false, true} {
		name := "http"
		if tls {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("printf agenterm-command-ok")) }))
			if tls {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			token, err := protocol.Encode(protocol.Token{URL: "ws" + strings.TrimPrefix(server.URL, "http") + "/client", Secret: randomBytes(protocol.TokenSecretSize)})
			if err != nil {
				t.Fatal(err)
			}
			shell, windows := installCommands(token)
			for _, cmd := range []string{shell, windows} {
				if strings.Contains(cmd, "https://") != tls || strings.Contains(cmd, "http://") {
					t.Fatalf("wrong protocol in command: %q", cmd)
				}
			}
			cmd := exec.Command("sh", "-c", shell)
			cmd.Env = append(os.Environ(), "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost")
			if tls {
				cert := server.TLS.Certificates[0].Certificate[0]
				file := filepath.Join(t.TempDir(), "server.pem")
				if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}), 0600); err != nil {
					t.Fatal(err)
				}
				cmd.Env = append(cmd.Env, "SSL_CERT_FILE="+file)
			}
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "agenterm-command-ok") {
				t.Fatalf("command failed: %v %s", err, out)
			}
		})
	}
}
