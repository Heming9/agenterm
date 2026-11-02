package service

import (
	"agenterm/internal/i18n"
	"agenterm/internal/installer"
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"encoding/base64"
	"fmt"
	"io/fs"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Downloads are available in every build, using the program's own version.
const downloadTag = "v" + version.Current

var validDownloadTag = regexp.MustCompile(`^v[0-9A-Za-z._+-]+$`)

// Six uppercase base36 characters carry about 31 random bits. The URL is itself
// an invitation credential, protected by the same expiry and failure limiter.
const installCodeLength = 6

// Use the last six base36 digits of the full random invitation secret.
// This keeps command formatting deterministic without exposing base64 symbols.
func installCode(secret string) string {
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(raw) != protocol.TokenSecretSize {
		return ""
	}
	code := strings.Repeat("0", installCodeLength) + strings.ToUpper(new(big.Int).SetBytes(raw).Text(36))
	return code[len(code)-installCodeLength:]
}

// The caller holds s.mu. Retry collisions before publishing either credential.
func (s *runtimeState) newInvitation(token string, expires time.Time) (string, error) {
	if s.invitations == nil {
		s.invitations = make(map[string]invitation)
	}
	if s.installCodes == nil {
		s.installCodes = make(map[string]string)
	}
	for {
		secret := randomBytes(protocol.TokenSecretSize)
		code := installCode(secret)
		if _, exists := s.installCodes[code]; exists {
			continue
		}
		if _, exists := s.invitations[secret]; exists {
			continue
		}
		value, err := protocol.Encode(protocol.Token{URL: s.clientURL, Secret: secret})
		if err != nil {
			return "", err
		}
		s.invitations[secret] = invitation{token: token, expires: expires}
		s.installCodes[code] = secret
		return value, nil
	}
}

// The caller holds s.mu. Remove short URLs with their underlying invitation.
func (s *runtimeState) deleteInvitation(secret string) {
	delete(s.invitations, secret)
	if code := installCode(secret); code != "" && s.installCodes[code] == secret {
		delete(s.installCodes, code)
	}
}

func installCommands(invitation string) (string, string) {
	endpoint, secret, err := invite.Endpoint(invitation)
	if err != nil {
		return "", ""
	}
	base := strings.TrimSuffix(endpoint, "/invite/redeem") + "/c/" + installCode(secret)
	// Command substitution leaves stdin connected to the terminal for consent.
	// Keep curl failure visible, rather than successfully executing an empty script.
	shell := fmt.Sprintf("s=$(curl -fsS -m15 %s)&&sh -c \"$s\"", shQuote(base))
	powershell := fmt.Sprintf("powershell -NoP -c \"iex(irm -TimeoutSec 15 %s)\"", psQuote(base+".ps1"))
	return shell, powershell
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Resolve short installer codes without changing the full invitation format.
// URLs carrying the full invitation secret are also supported.
func (s *frontend) installWrapper(tag string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, i18n.Text(i18n.Terminal(), "method.not.allowed"), http.StatusMethodNotAllowed)
			return
		}
		if !validDownloadTag.MatchString(tag) || !strings.HasPrefix(r.URL.Path, "/c/") {
			http.NotFound(w, r)
			return
		}
		secret, powershell := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/c/"), ".ps1")
		entry, err := controlCall[installerInvitation](r.Context(), s.control, "invitation", invitationRequest{Identity: requestIdentity{Header: r.Header, RemoteAddr: r.RemoteAddr}, Secret: secret})
		if err != nil {
			writeControlError(w, err)
			return
		}
		secret = entry.Secret
		source, err := protocol.Decode(entry.Token)
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
		if !validDownloadTag.MatchString(tag) || !strings.HasPrefix(r.URL.Path, prefix) {
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

var downloadArchive = regexp.MustCompile(`^agenterm-client-cli-((macos|linux)-(amd64|arm64)\.tar\.gz|windows-(amd64|arm64)\.zip)$`)
