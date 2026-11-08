package service

import (
	"agenterm/internal/components"
	"agenterm/internal/i18n"
	"agenterm/internal/installer"
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Downloads are available in every build, using the program's own version.
const downloadTag = "v" + version.Current

var validDownloadTag = regexp.MustCompile(`^v[0-9A-Za-z._+-]+$`)
var validInstallCredential = regexp.MustCompile(`^(?:[A-Z0-9]{6}|[A-Za-z0-9_-]{14})$`)
var safeShellURL = regexp.MustCompile(`^[A-Za-z0-9._~:/%-]+$`)

const shortCodeLength = 6

// The caller holds s.mu.
func (s *runtimeState) newInvitation(token, identifier string, expires time.Time) (string, error) {
	if s.invitations == nil {
		s.invitations = make(map[string]invitation)
	}
	for {
		secret := randomBytes(protocol.TokenSecretSize)
		if _, exists := s.invitations[secret]; exists {
			continue
		}
		value, err := protocol.Encode(protocol.Token{URL: s.clientURL, Secret: secret, Identifier: identifier})
		if err != nil {
			return "", err
		}
		s.invitations[secret] = invitation{token: token, expires: expires}
		return value, nil
	}
}

func installCommands(token string) (string, string) {
	endpoint, secret, err := invite.Endpoint(token)
	if err != nil {
		return "", ""
	}
	base := strings.TrimSuffix(endpoint, "/invite/redeem") + "/c/" + secret
	commandBase := base
	if u, err := url.Parse(base); err == nil && u.Scheme == "https" && u.Host == "agenterm.net" {
		commandBase = strings.TrimPrefix(base, "https://")
	}
	shellBase := strings.TrimPrefix(commandBase, "http://")
	shellURL := shellBase
	if !safeShellURL.MatchString(shellURL) {
		shellURL = shQuote(shellURL)
	}
	shell := fmt.Sprintf("curl -fL %s|sh", shellURL)
	powershell := "powershell -NoP " + psQuote("irm "+commandBase+".ps1|iex")
	return shell, powershell
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// GET serves the installer; POST resolves an independent short code to a token.
// GET also accepts a client-token secret or a legacy full invitation secret.
func (s *frontend) installWrapper(tag string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, HEAD, POST")
			http.Error(w, i18n.Text(i18n.Terminal(), "method.not.allowed"), http.StatusMethodNotAllowed)
			return
		}
		if !validDownloadTag.MatchString(tag) || !strings.HasPrefix(r.URL.Path, "/c/") {
			http.NotFound(w, r)
			return
		}
		credential, powershell := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/c/"), ".ps1")
		if !validInstallCredential.MatchString(credential) || (r.Method == http.MethodPost && (powershell || len(credential) != shortCodeLength)) {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost && !acceptClientVersion(w, r) {
			return
		}
		entry, err := controlCall[installerInvitation](r.Context(), s.control, "invitation", invitationRequest{Identity: requestIdentity{Header: r.Header, RemoteAddr: r.RemoteAddr}, Secret: credential, Consume: r.Method == http.MethodPost})
		if r.Method == http.MethodPost {
			if err != nil {
				writeControlError(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"token": entry.Token})
			return
		}
		value := ""
		clientURL := s.clientURL
		if err != nil {
			var ce *controlError
			if !errors.As(err, &ce) || ce.Status != http.StatusUnauthorized {
				writeControlError(w, err)
				return
			}
		} else {
			source, decodeErr := protocol.Decode(entry.Token)
			if decodeErr == nil {
				clientURL = source.URL
				if len(credential) == shortCodeLength {
					u, e := url.Parse(source.URL)
					decodeErr = e
					if e == nil {
						if u.Scheme == "wss" {
							u.Scheme = "https"
						} else {
							u.Scheme = "http"
						}
						u.Path = strings.TrimSuffix(u.Path, "/client") + "/c/" + credential
						value = u.String()
					}
				} else if entry.DeviceToken {
					value = entry.Token
				} else {
					value, decodeErr = protocol.Encode(protocol.Token{URL: source.URL, Secret: entry.Secret, Identifier: source.Identifier})
				}
			}
			if decodeErr != nil {
				http.Error(w, i18n.Text(i18n.Terminal(), "invalid.invitation"), http.StatusInternalServerError)
				return
			}
		}
		u, parseErr := url.Parse(clientURL)
		if parseErr != nil || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") {
			http.Error(w, i18n.Text(i18n.Terminal(), "installer.unavailable"), http.StatusServiceUnavailable)
			return
		}
		if u.Scheme == "wss" {
			u.Scheme = "https"
		} else {
			u.Scheme = "http"
		}
		u.Path = strings.TrimSuffix(u.Path, "/client") + "/downloads/" + tag
		base := u.String()
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
		kind := "invitation"
		if entry.DeviceToken {
			kind = "token"
		}
		if powershell {
			body = "& {\n" + body + "\n} " + psQuote(value) + " " + psQuote(base) + " " + psQuote(kind) + "\n"
		} else {
			body = "#!/bin/sh\n{\nset -- " + shQuote(value) + " " + shQuote(base) + " " + shQuote(kind) + "\n" + strings.TrimPrefix(body, "#!/bin/sh\n") + "\n}\n"
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(body))
		}
	})
}

// Only this server's version and the release download filenames are public.
// Source executables, selected archive formats and their checksums are public.
func downloadHandler(tag string, assets fs.FS) http.Handler {
	assets = components.Downloads(assets, "agenterm-client-cli")
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
		if name != "SHA256SUMS" && !downloadArchive.MatchString(strings.TrimSuffix(name, ".sha256")) {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, assets, name)
	})
}

var downloadArchive = regexp.MustCompile(`^agenterm-client-cli-((macos|linux)-(amd64|arm64)(\.(tar\.xz|tar\.gz))?|windows-(amd64|arm64)(\.exe|\.tar\.xz|\.zip))$`)
