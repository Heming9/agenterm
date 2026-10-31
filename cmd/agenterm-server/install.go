package main

import (
	"agenterm/internal/i18n"
	"agenterm/internal/installer"
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Set by the release workflow to keep the installer and binaries on the same tag.
var releaseTag = ""
var validReleaseTag = regexp.MustCompile(`^v[0-9A-Za-z._+-]+$`)

func printInstallCommands(index int, invitation string) {
	if !validReleaseTag.MatchString(releaseTag) {
		fmt.Printf(serverText("invitation.d.expires.in.15.minutes.s"), index, invitation)
		return
	}
	shell, powershell := installCommands(invitation)
	fmt.Printf(serverText("invitation.d.expires.in.15.minutes.share"), index)
	fmt.Printf(serverText("macos.linux.s.windows.powershell.s"), shell, powershell)
}

func installCommands(invitation string) (string, string) {
	endpoint, secret, err := invite.Endpoint(invitation)
	if err != nil {
		return "", ""
	}
	base := strings.TrimSuffix(endpoint, "/invite/redeem") + "/c/" + secret
	// Command substitution leaves stdin connected to the terminal for consent.
	// Keep curl failure visible, rather than successfully executing an empty script.
	shell := fmt.Sprintf("s=$(curl -fsS -m15 %s)&&sh -c \"$s\"", shQuote(base))
	powershell := fmt.Sprintf("iex(irm -TimeoutSec 15 %s)", psQuote(base+".ps1"))
	return shell, powershell
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// The URL carries the existing 128-bit invitation secret. It has the same
// expiry and single redemption as the full invitation; no alias state is needed.
func (s *server) installWrapper(tag string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, i18n.Text(i18n.Terminal(), "method.not.allowed"), http.StatusMethodNotAllowed)
			return
		}
		if !validReleaseTag.MatchString(tag) || !strings.HasPrefix(r.URL.Path, "/c/") {
			http.NotFound(w, r)
			return
		}
		secret, powershell := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/c/"), ".ps1")
		s.mu.Lock()
		entry, exists := s.invitations[secret]
		s.mu.Unlock()
		if status, err := s.authorize(r.RemoteAddr, exists && time.Now().Before(entry.expires)); err != nil {
			if status == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "300")
				http.Error(w, err.Error(), status)
				return
			}
			http.Error(w, i18n.Text(i18n.Terminal(), "invitation.invalid.expired.or.already.used"), status)
			return
		}
		source, err := protocol.Decode(entry.token)
		if err != nil {
			http.Error(w, i18n.Text(i18n.Terminal(), "invalid.invitation"), http.StatusInternalServerError)
			return
		}
		value, err := protocol.Encode(protocol.Token{URL: source.URL, Secret: secret})
		if err != nil {
			http.Error(w, i18n.Text(i18n.Terminal(), "invalid.invitation"), http.StatusInternalServerError)
			return
		}
		endpoint, _, _ := invite.Endpoint(value)
		base := strings.TrimSuffix(endpoint, "/invite/redeem") + "/downloads/" + tag
		name := "install.sh"
		if powershell {
			name = "install.ps1"
		}
		script, err := installer.Script(name)
		if err != nil {
			http.Error(w, i18n.Text(i18n.Terminal(), "installer.unavailable"), http.StatusInternalServerError)
			return
		}
		body := strings.ReplaceAll(string(script), "@RELEASE_TAG@", tag)
		if powershell {
			body = "& {\n" + body + "\n} " + psQuote(value) + " " + psQuote(base) + "\n"
		} else {
			body = "#!/bin/sh\nset -- " + shQuote(value) + " " + shQuote(base) + "\n" + strings.TrimPrefix(body, "#!/bin/sh\n")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(body))
		}
	})
}

// Only this server's version and the release download filenames are public.
// No directory listings or uncompressed executables are exposed.
func downloadHandler(tag string, assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, i18n.Text(i18n.Terminal(), "method.not.allowed"), http.StatusMethodNotAllowed)
			return
		}
		prefix := "/downloads/" + tag + "/"
		if !validReleaseTag.MatchString(tag) || !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, prefix)
		if name == "install.sh" || name == "install.ps1" {
			script, err := installer.Script(name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			http.ServeContent(w, r, name, time.Time{}, strings.NewReader(strings.ReplaceAll(string(script), "@RELEASE_TAG@", tag)))
			return
		}
		if name != "SHA256SUMS" && !downloadArchive.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, assets, name)
	})
}

var downloadArchive = regexp.MustCompile(`^agenterm-cli-((macos|linux)-(amd64|arm64)\.tar\.gz|windows-(amd64|arm64)\.zip)$`)
