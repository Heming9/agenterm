package service

import (
	"agenterm/internal/components"
	"agenterm/internal/i18n"
	"agenterm/internal/installer"
	"agenterm/internal/invite"
	"agenterm/internal/protocol"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

var validDownloadTag = regexp.MustCompile(`^v[0-9A-Za-z._+-]+$`)
var validInstallCredential = regexp.MustCompile(`^(?:[A-Za-z0-9]{6}|[A-Za-z0-9_-]{14})$`)
var safeShellURL = regexp.MustCompile(`^[A-Za-z0-9._~:/%-]+$`)
var safeWindowsURL = regexp.MustCompile(`^[A-Za-z0-9._~:/-]+$`)

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

// connectionPageURL derives the public page address from the device credential.
func connectionPageURL(token, code string) (*url.URL, error) {
	endpoint, secret, err := invite.Endpoint(token)
	if err != nil {
		return nil, err
	}
	if code != "" {
		if len(code) != shortCodeLength || !validInstallCredential.MatchString(code) {
			return nil, fmt.Errorf("invalid short code")
		}
		secret = code
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimSuffix(u.Path, "/invite/redeem") + "/c/" + secret
	u.RawPath = ""
	return u, nil
}

func installAddress(u *url.URL) string {
	if u.Scheme == "https" {
		return u.String()
	}
	return u.Host + u.EscapedPath()
}

func installCommands(token, code string) (string, string) {
	u, err := connectionPageURL(token, code)
	if err != nil {
		return "", ""
	}
	return installCommandsForURL(u)
}

func installCommandsForURL(u *url.URL) (string, string) {
	address := installAddress(u)
	shellURL := address + ".sh"
	if !safeShellURL.MatchString(shellURL) {
		shellURL = shQuote(shellURL)
	}
	shell := fmt.Sprintf("curl -fL %s|sh", shellURL)
	powershell := `powershell -NoP "irm ` + address + `.ps1|iex"`
	if !safeWindowsURL.MatchString(address) {
		// Avoid cmd expansion and shell metacharacters in uncommon URLs.
		script := "irm " + psQuote(address+".ps1") + "|iex"
		code := utf16.Encode([]rune(script))
		bytes := make([]byte, len(code)*2)
		for i, value := range code {
			binary.LittleEndian.PutUint16(bytes[i*2:], value)
		}
		powershell = "powershell -NoP -EncodedCommand " + base64.StdEncoding.EncodeToString(bytes)
	}
	return shell, powershell
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func installerExtension(userAgent string) string {
	userAgent = strings.ToLower(strings.TrimSpace(userAgent))
	if strings.HasPrefix(userAgent, "curl/") {
		return ".sh"
	}
	for product := range strings.FieldsSeq(userAgent) {
		if strings.HasPrefix(product, "powershell/") || strings.HasPrefix(product, "windowspowershell/") {
			return ".ps1"
		}
	}
	return ""
}

// Device credentials and short-code aliases share the invitation routes.
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
		credential := strings.TrimPrefix(r.URL.Path, "/c/")
		extension := path.Ext(credential)
		credential = strings.TrimSuffix(credential, extension)
		redeem := r.Method == http.MethodPost
		if !validInstallCredential.MatchString(credential) ||
			(extension != "" && (redeem || (extension != ".sh" && extension != ".ps1"))) {
			http.NotFound(w, r)
			return
		}
		if redeem && !acceptClientVersion(w, r) {
			return
		}
		entry, err := controlCall[installerInvitation](r.Context(), s.control, "invitation", invitationRequest{
			Identity: requestIdentity{Header: r.Header, RemoteAddr: r.RemoteAddr}, Secret: credential,
			Consume: redeem, TemporaryInstaller: true,
		})
		if err != nil {
			writeControlError(w, err)
			return
		}
		code := ""
		if len(credential) == shortCodeLength {
			code = credential
		} else if !entry.DeviceToken {
			http.Error(w, i18n.Text(i18n.Terminal(), "invalid.invitation"), http.StatusUnauthorized)
			return
		}
		if redeem {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"token": entry.Token})
			return
		}
		if extension == "" {
			w.Header().Add("Vary", "User-Agent")
			extension = installerExtension(r.UserAgent())
		}
		if extension == "" {
			serveInvitationPage(w, r, entry, code)
			return
		}
		source, decodeErr := protocol.Decode(entry.Token)
		if decodeErr != nil {
			http.Error(w, i18n.Text(i18n.Terminal(), "invalid.invitation"), http.StatusInternalServerError)
			return
		}
		value := entry.Token
		clientURL := source.URL
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
		script, err := installer.Script("install" + extension)
		if err != nil {
			http.Error(w, i18n.Text(i18n.Terminal(), "installer.unavailable"), http.StatusInternalServerError)
			return
		}
		body := strings.ReplaceAll(string(script), "@RELEASE_TAG@", tag)
		args := " 'token'"
		if entry.TemporaryAssistance {
			args += " 'temporary-assistance'"
		}
		if extension == ".ps1" {
			body = "& {\n" + body + "\n} " + psQuote(value) + " " + psQuote(base) + args + "\n"
		} else {
			body = "#!/bin/sh\n{\nset -- " + shQuote(value) + " " + shQuote(base) + args + "\n" + strings.TrimPrefix(body, "#!/bin/sh\n") + "\n}\n"
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(body))
		}
	})
}

// Only the bundled Client CLI version and its release download filenames are public.
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
