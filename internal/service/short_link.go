package service

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"agenterm/internal/theme"
	"agenterm/internal/version"

	"golang.org/x/text/language"
)

// browserInvitationURL uses the same scheme convention as the installer.
func browserInvitationURL(token, code string) string {
	u, err := connectionPageURL(token, code)
	if err != nil {
		return ""
	}
	return installAddress(u)
}

//go:embed assets/short-link.html
var shortLinkHTML string

var shortLinkPage = template.Must(template.New("connection").Parse(
	strings.Replace(shortLinkHTML, "<style>", "<style>\n"+theme.WebCSS(), 1),
))

// Hash the exact inline script so the page can retain a restrictive CSP.
var shortLinkScriptHash = func() string {
	script := strings.SplitN(strings.SplitN(shortLinkHTML, "<script>", 2)[1], "</script>", 2)[0]
	digest := sha256.Sum256([]byte(script))
	return base64.StdEncoding.EncodeToString(digest[:])
}()

func serveInvitationPage(w http.ResponseWriter, r *http.Request, entry installerInvitation, code string) {
	w.Header().Add("Vary", "Accept-Language")
	w.Header().Add("Vary", "Accept-Encoding")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; script-src 'sha256-"+shortLinkScriptHash+"'; base-uri 'none'; frame-ancestors 'none'")
	pageURL, err := connectionPageURL(entry.Token, code)
	if err != nil {
		http.Error(w, "Invalid connection endpoint", http.StatusInternalServerError)
		return
	}
	shell, windows := installCommands(entry.Token, code)
	query := url.Values{"url": {pageURL.String()}}.Encode()
	cliURL := "agenterm-cli://connect?" + query
	lang := shortLinkLanguage(r.Header.Get("Accept-Language"))
	switch r.URL.Query().Get("lang") {
	case "en":
		lang = "en"
	case "zh-CN":
		lang = "zh-CN"
	}
	baseURL := *pageURL
	baseURL.Path = baseURL.Path[:strings.LastIndex(baseURL.Path, "/c/")]
	baseURL.RawPath = ""
	serverAddress := baseURL.String()
	validity := connectionValidity(entry)
	label, remaining := countdown(entry, time.Now())
	if lang == "en" {
		validity = englishConnectionValidity(entry)
		if label != "" {
			label = "Expires in "
		}
	}
	fallback := ""
	if label != "" {
		fallback = validity
	}
	var rendered bytes.Buffer
	if err := shortLinkPage.Execute(&rendered, struct {
		Windows, Shell, Token, Version, Server string
		Language                               string
		Text                                   map[string]string
		CountdownLabel, CountdownFallback      string
		Remaining                              int64
		CLIURL                                 template.URL
		TemporaryAssistance                    bool
	}{windows, shell, entry.Token, version.Current, serverAddress, lang, shortLinkTexts[lang], label, fallback, remaining, template.URL(cliURL), entry.TemporaryAssistance}); err != nil {
		http.Error(w, "Unable to render invitation page", http.StatusInternalServerError)
		return
	}
	body := rendered.Bytes()
	if acceptsGzip(r.Header.Values("Accept-Encoding")) {
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		_, writeErr := writer.Write(body)
		closeErr := writer.Close()
		if writeErr != nil || closeErr != nil {
			http.Error(w, "Unable to compress invitation page", http.StatusInternalServerError)
			return
		}
		body = compressed.Bytes()
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

// The runtime tracks the first-connection deadline and later offline expiry.
func connectionValidity(entry installerInvitation) string {
	deadline, err := time.Parse(time.RFC3339, entry.TokenExpiresAt)
	if err != nil {
		return "暂时无法获取到期时间，请联系邀请发起人确认。"
	}
	deadlineText := deadline.In(time.FixedZone("北京时间", 8*60*60)).Format("2006-01-02 15:04:05") + "（北京时间）"
	if entry.TokenState == "offline" {
		return "设备已断开，连接令牌和一键命令有效至 " + deadlineText + "；过期后请重新生成连接信息。"
	}
	if entry.TokenState == "initial" {
		return "首次连接前，连接令牌和一键命令有效至 " + deadlineText + "；过期后请重新生成连接信息，已连接设备不受影响。"
	}
	return "暂时无法获取到期时间，请联系邀请发起人确认。"
}

// countdown uses the token expiry reported when the page is loaded.
func countdown(entry installerInvitation, now time.Time) (string, int64) {
	deadline, err := time.Parse(time.RFC3339, entry.TokenExpiresAt)
	if err != nil {
		return "", 0
	}
	label := ""
	switch entry.TokenState {
	case "initial", "offline":
		label = "有效期剩余 "
	default:
		return "", 0
	}
	duration := deadline.Sub(now)
	if duration <= 0 {
		return label, 0
	}
	return label, int64((duration + time.Second - 1) / time.Second)
}

// shortLinkLanguage chooses Chinese only when it is the browser's top preference.
func shortLinkLanguage(header string) string {
	tags, _, err := language.ParseAcceptLanguage(header)
	if err != nil || len(tags) == 0 {
		return "en"
	}
	base, _ := tags[0].Base()
	if base.String() == "zh" {
		return "zh-CN"
	}
	return "en"
}

func englishConnectionValidity(entry installerInvitation) string {
	deadline, err := time.Parse(time.RFC3339, entry.TokenExpiresAt)
	if err != nil || (entry.TokenState != "initial" && entry.TokenState != "offline") {
		return "Expiration time unavailable. Please contact the sender."
	}
	return "Token expires at " + deadline.UTC().Format("2006-01-02 15:04:05") + " UTC."
}

// Explicit gzip preferences override the wildcard; q=0 forbids compression.
func acceptsGzip(headers []string) bool {
	gzipSeen, gzipOK, wildcardOK := false, false, false
	for _, header := range headers {
		for _, item := range strings.Split(header, ",") {
			parts := strings.Split(item, ";")
			encoding := strings.ToLower(strings.TrimSpace(parts[0]))
			if encoding != "gzip" && encoding != "*" {
				continue
			}
			q := 1.0
			for _, parameter := range parts[1:] {
				key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
				if ok && strings.EqualFold(strings.TrimSpace(key), "q") {
					parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
					if err != nil || parsed < 0 || parsed > 1 {
						q = 0
					} else {
						q = parsed
					}
				}
			}
			if encoding == "gzip" {
				gzipSeen, gzipOK = true, q > 0
			} else {
				wildcardOK = q > 0
			}
		}
	}
	if gzipSeen {
		return gzipOK
	}
	return wildcardOK
}
