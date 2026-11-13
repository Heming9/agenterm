package service

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"agenterm/internal/protocol"
	"agenterm/internal/version"
)

func TestShortLinkPage(t *testing.T) {
	for _, prefix := range []string{"", "/tools/remote"} {
		t.Run(prefix, func(t *testing.T) {
			s := &runtimeState{enableShortCode: true}
			ts := httptest.NewUnstartedServer(nil)
			defer ts.Close()
			origin := "http://" + ts.Listener.Addr().String()
			s.clientURL = "ws" + strings.TrimPrefix(origin, "http") + prefix + "/client"
			ts.Config.Handler = s.routes("v1.2.3", fstest.MapFS{})
			ts.Start()
			browser := func(method, address string) (*http.Response, error) {
				request, err := http.NewRequest(method, address, nil)
				if err != nil {
					return nil, err
				}
				request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
				request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
				return ts.Client().Do(request)
			}
			issued, err := s.createClient("")
			if err != nil {
				t.Fatal(err)
			}
			code := issued["short_code"].(string)
			link := issued["short_code_url"].(string)
			if link != strings.TrimPrefix(origin, "http://")+prefix+"/c/"+code {
				t.Fatalf("link: %s", link)
			}
			link = "http://" + link
			for _, tc := range []struct{ encoding, want string }{
				{"gzip", "gzip"}, {"gzip;q=0", ""}, {"*;q=1, gzip;q=0", ""}, {"br, gzip;q=0.7", "gzip"},
			} {
				req, err := http.NewRequest(http.MethodGet, link, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Accept-Encoding", tc.encoding)
				req.Header.Set("Accept-Language", "zh-CN")
				res, err := ts.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if res.Header.Get("Content-Encoding") != tc.want {
					t.Fatalf("encoding %q got %q", tc.encoding, res.Header.Get("Content-Encoding"))
				}
				if !strings.Contains(strings.Join(res.Header.Values("Vary"), ","), "Accept-Encoding") {
					t.Fatal("missing Accept-Encoding Vary")
				}
				if tc.want == "gzip" {
					zr, err := gzip.NewReader(bytes.NewReader(data))
					if err != nil {
						t.Fatal(err)
					}
					expanded, err := io.ReadAll(zr)
					if err != nil {
						t.Fatal(err)
					}
					if err := zr.Close(); err != nil {
						t.Fatal(err)
					}
					if len(data) >= len(expanded) {
						t.Fatalf("gzip did not reduce payload: %d >= %d", len(data), len(expanded))
					}
					data = expanded
				}
				content := string(data)
				scriptStart := strings.Index(content, "<script>")
				scriptEnd := strings.Index(content, "</script>")
				if scriptStart < 0 || scriptEnd < scriptStart {
					t.Fatal("missing inline script")
				}
				digest := sha256.Sum256([]byte(content[scriptStart+len("<script>") : scriptEnd]))
				if !strings.Contains(res.Header.Get("Content-Security-Policy"), "'sha256-"+base64.StdEncoding.EncodeToString(digest[:])+"'") {
					t.Fatal("script CSP hash does not match response")
				}
				if !strings.Contains(content, `src="data:image/webp;base64,`) || !strings.Contains(content, "打开桌面版并连接") {
					t.Fatal("incorrect page content")
				}
			}
			reqHead, _ := http.NewRequest(http.MethodHead, link, nil)
			reqHead.Header.Set("Accept-Encoding", "gzip")
			headRes, err := ts.Client().Do(reqHead)
			if err != nil {
				t.Fatal(err)
			}
			headData, err := io.ReadAll(headRes.Body)
			headRes.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if headRes.Header.Get("Content-Encoding") != "gzip" || len(headData) != 0 || headRes.ContentLength <= 0 {
				t.Fatalf("invalid gzip HEAD response: %v", headRes.Header)
			}

			for _, tc := range []struct{ query, browserLanguage, htmlLanguage, activeLabel string }{
				{"?lang=en", "zh-CN", "en", "English"},
				{"?lang=zh-CN", "en-US", "zh-CN", "中文"},
				{"?lang=unsupported", "zh-CN", "zh-CN", "中文"},
			} {
				req, err := http.NewRequest(http.MethodGet, link+tc.query, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Accept-Language", tc.browserLanguage)
				res, err := ts.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				body := string(data)
				if res.StatusCode != http.StatusOK || !strings.Contains(body, `<html lang="`+tc.htmlLanguage+`">`) || !strings.Contains(body, `aria-current="page">`+tc.activeLabel+`</a>`) || !strings.Contains(body, `href="?lang=en"`) || !strings.Contains(body, `href="?lang=zh-CN"`) {
					t.Fatalf("language switch %q: status=%d body=%s", tc.query, res.StatusCode, body)
				}
			}

			for _, languageHeader := range []string{"en-US,en;q=0.9", "fr-FR,fr;q=0.9", ""} {
				req, err := http.NewRequest(http.MethodGet, link, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Accept-Language", languageHeader)
				res, err := ts.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				body := string(data)
				english := html.UnescapeString(body)
				if !strings.Contains(english, "Server: ") || !strings.Contains(english, "<strong>"+origin+prefix+"</strong>") || !strings.Contains(english, "accessing or modifying files") {
					t.Fatal("missing English authorization details")
				}
				if res.StatusCode != http.StatusOK || !strings.Contains(body, `<html lang="en">`) || !strings.Contains(body, "Request to connect to this computer") || !strings.Contains(body, "How to use?") || !strings.Contains(body, "Expires in ") || strings.Contains(body, "如何使用") || !strings.Contains(res.Header.Get("Vary"), "Accept-Language") {
					t.Fatalf("language %q: status=%d body=%s", languageHeader, res.StatusCode, body)
				}
			}
			for i := 0; i < 2; i++ {
				res, err := browser(http.MethodGet, link)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(res.Body)
				res.Body.Close()
				text := html.UnescapeString(string(body))
				if !strings.Contains(text, "允许授权（临时连接）") || !strings.Contains(text, "允许授权（持久连接）") || !strings.Contains(text, "拒绝授权") {
					t.Fatal("missing authorization options")
				}
				if !strings.Contains(text, "服务器：") || !strings.Contains(text, "<strong>"+origin+prefix+"</strong>") || !strings.Contains(text, "访问和修改文件") {
					t.Fatal("missing Chinese authorization details")
				}
				if !strings.Contains(text, `src="data:image/webp;base64,`) || !strings.Contains(text, "<script>") || strings.Contains(text, "short-link-countdown.js") || !strings.Contains(res.Header.Get("Content-Security-Policy"), "'sha256-"+shortLinkScriptHash+"'") {
					t.Fatal("page is not self-contained")
				}
				if !strings.Contains(text, `class="brand-version">v`+version.Current+`</span>`) {
					t.Fatal("missing current version")
				}
				wantAppURL := "agenterm://connect?" + url.Values{"url": {link}}.Encode()
				if !strings.Contains(text, `data-url="`+wantAppURL+`"`) || !strings.Contains(text, "打开桌面版并连接") {
					t.Fatalf("missing deep link %q: %s", wantAppURL, text)
				}
				wantCLIURL := "agenterm-cli://connect?" + url.Values{"url": {link}}.Encode()
				if !strings.Contains(text, `data-url="`+wantCLIURL+`"`) || !strings.Contains(text, "打开终端版并连接") {
					t.Fatalf("missing CLI deep link %q", wantCLIURL)
				}
				if res.StatusCode != http.StatusOK || !strings.Contains(text, issued["client_token"].(string)) || !strings.Contains(text, issued["windows_powershell_command"].(string)) || !strings.Contains(text, issued["macos_linux_command"].(string)) || strings.Contains(text, issued["control_token"].(string)) {
					t.Fatalf("page status %d: %s", res.StatusCode, body)
				}
				expires, err := time.Parse(time.RFC3339, issued["token_initial_expires_at"].(string))
				if err != nil {
					t.Fatal(err)
				}
				expected := expires.In(time.FixedZone("北京时间", 8*60*60)).Format("2006-01-02 15:04:05") + "（北京时间）"
				if !strings.Contains(text, "请在需要远程协助的电脑上打开此页面") || !strings.Contains(text, "有效期") || !strings.Contains(text, expected) || strings.Contains(text, "短码最多") {
					t.Fatalf("missing instructions or deadline: %s", text)
				}
				if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Referrer-Policy") != "no-referrer" {
					t.Fatal("missing privacy headers")
				}
			}
			head, err := browser(http.MethodHead, link)
			if err != nil {
				t.Fatal(err)
			}
			headBody, _ := io.ReadAll(head.Body)
			head.Body.Close()
			if head.StatusCode != http.StatusOK || len(headBody) != 0 || !strings.HasPrefix(head.Header.Get("Content-Type"), "text/html") {
				t.Fatalf("HEAD status=%d body=%q", head.StatusCode, headBody)
			}
			unsupported, _ := http.NewRequest(http.MethodPut, link, nil)
			response, err := ts.Client().Do(unsupported)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Allow") != "GET, HEAD, POST" {
				t.Fatalf("PUT status=%d", response.StatusCode)
			}
			for i := 0; i < 10; i++ {
				req, _ := http.NewRequest(http.MethodPost, origin+prefix+"/c/"+code, nil)
				req.Header.Set(version.Header, version.Current)
				res, err := ts.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				if res.StatusCode != http.StatusOK {
					t.Fatalf("redemption %d: %d", i+1, res.StatusCode)
				}
				if i < 9 {
					res, err := browser(http.MethodGet, link)
					if err != nil {
						t.Fatal(err)
					}
					res.Body.Close()
					if res.StatusCode != http.StatusOK {
						t.Fatalf("link after redemption %d: %d", i+1, res.StatusCode)
					}
				}
			}
			res, err := browser(http.MethodGet, link)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != http.StatusUnauthorized {
				t.Fatalf("used link: %d", res.StatusCode)
			}
		})
	}
}

func TestShortLinkExpiry(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	code := issued["short_code"].(string)
	s.mu.Lock()
	token := issued["client_token"].(string)
	s.clientOwners[s.tokens[token]].expires = time.Now().Add(-time.Second)
	s.mu.Unlock()
	w := httptest.NewRecorder()
	s.testFrontend().shortLink(w, httptest.NewRequest(http.MethodGet, "/c/"+code, nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired link: %d", w.Code)
	}
}

func TestShortLinkDisabled(t *testing.T) {
	s := &runtimeState{clientURL: "wss://example.com/client"}
	w := httptest.NewRecorder()
	s.testFrontend().shortLink(w, httptest.NewRequest(http.MethodGet, "/c/abcdef", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled short link: %d", w.Code)
	}
}

func TestShortLinkDoesNotConflictWithClientRoute(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/agenterm/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.shortCodes["client"] = shortCodeEntry{token: issued["client_token"].(string)}
	s.mu.Unlock()
	handler := s.routes("v1.2.3", fstest.MapFS{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agenterm/c/client", nil)
	req.Header.Set("Accept", "text/html")
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), issued["client_token"].(string)) {
		t.Fatalf("short link status: %d", w.Code)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/agenterm/client", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("client route status: %d", w.Code)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/agenterm/abcdef", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("old root link status: %d", w.Code)
	}
}

func TestShortLinkRouteIsRedacted(t *testing.T) {
	if got := publicRoute("/agenterm/c/abcdef", "/agenterm"); got != "/c/{code}" {
		t.Fatalf("route: %q", got)
	}
}

func TestShortCodeGetNeverServesInstaller(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	code := issued["short_code"].(string)
	handler := s.installWrapper("v1.2.3")
	for _, accept := range []string{"", "text/html", "*/*", "text/html;q=0"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			r := httptest.NewRequest(method, "/c/"+code, nil)
			if accept != "" {
				r.Header.Set("Accept", accept)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
				t.Fatalf("%s %q: status=%d type=%q", method, accept, w.Code, w.Header().Get("Content-Type"))
			}
			if strings.Contains(w.Body.String(), "#!/bin/sh") {
				t.Fatal("short code served installer")
			}
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/c/"+code+".ps1", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("short PowerShell script: %d", w.Code)
	}
}

func TestConnectionValidityStates(t *testing.T) {
	deadline := "2026-11-10T13:18:11Z"
	for _, tc := range []struct{ state, expiry, want string }{
		{"initial", deadline, "首次连接前，连接令牌和一键命令有效至 2026-11-10 21:18:11（北京时间）"},
		{"offline", deadline, "设备已断开，连接令牌和一键命令有效至 2026-11-10 21:18:11（北京时间）"},
		{"", "", "暂时无法获取到期时间"},
	} {
		got := connectionValidity(installerInvitation{TokenState: tc.state, TokenExpiresAt: tc.expiry})
		if !strings.Contains(got, tc.want) {
			t.Errorf("state %q got %q", tc.state, got)
		}
	}
}

func TestShortLinkShowsCurrentTokenLifetime(t *testing.T) {
	h := newMultiHarness(t, 0)
	issued := h.call(t, "admin-test", "create_client", map[string]any{"timeout_seconds": 3600}, false).(map[string]any)
	token, code := issued["client_token"].(string), issued["short_code"].(string)
	read := func() string {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/c/"+code, nil)
		req.Header.Set("Accept-Language", "zh-CN")
		h.s.testFrontend().shortLink(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("page status: %d", w.Code)
		}
		return html.UnescapeString(w.Body.String())
	}
	if page := read(); !strings.Contains(page, "首次连接前") {
		t.Fatal("initial deadline missing")
	}
	conn, _ := h.connect(t, token, "validity")
	w := httptest.NewRecorder()
	h.s.testFrontend().shortLink(w, httptest.NewRequest(http.MethodGet, "/c/"+code, nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("online link status: %d", w.Code)
	}
	conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.s.mu.Lock()
		online := len(h.s.clients) != 0
		h.s.mu.Unlock()
		if !online {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnect not processed")
		}
		time.Sleep(time.Millisecond)
	}
	if page := read(); !strings.Contains(page, "设备已断开") || !strings.Contains(page, "（北京时间）") {
		t.Fatal("offline deadline missing")
	}
}

func TestShortCodeAddressScheme(t *testing.T) {
	for _, tc := range []struct{ clientURL, want string }{
		{"wss://agenterm.net/client", "https://agenterm.net/c/abcdef"},
		{"wss://example.com/remote/client", "https://example.com/remote/c/abcdef"},
		{"ws://example.com:8080/remote/client", "example.com:8080/remote/c/abcdef"},
		{"ws://[::1]:8080/client", "[::1]:8080/c/abcdef"},
	} {
		token, err := protocol.Encode(protocol.Token{URL: tc.clientURL, Secret: randomBytes(protocol.TokenSecretSize)})
		if err != nil {
			t.Fatal(err)
		}
		if got := shortCodeURL(token, "abcdef"); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.clientURL, got, tc.want)
		}
	}
}

func TestCountdownSnapshot(t *testing.T) {
	now := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		state, expiry, label string
		remaining            int64
	}{
		{"initial", "2026-11-13T08:05:00Z", "有效期剩余 ", 300},
		{"offline", "2026-11-13T08:00:01Z", "有效期剩余 ", 1},
		{"initial", "2026-11-13T07:59:59Z", "有效期剩余 ", 0},
		{"initial", "invalid", "", 0},
		{"unknown", "2026-11-13T08:05:00Z", "", 0},
	} {
		label, remaining := countdown(installerInvitation{TokenState: tc.state, TokenExpiresAt: tc.expiry}, now)
		if label != tc.label || remaining != tc.remaining {
			t.Errorf("%q: got %q %d", tc.state, label, remaining)
		}
	}
}

func TestShortLinkLanguage(t *testing.T) {
	for _, tc := range []struct{ header, want string }{
		{"zh-CN,zh;q=0.9,en;q=0.8", "zh-CN"},
		{"zh-TW,zh;q=0.9", "zh-CN"},
		{"en-US,en;q=0.9,zh;q=0.8", "en"},
		{"fr-FR,zh;q=0.8", "en"},
		{"zh;q=0,en;q=1", "en"},
		{"", "en"},
		{"invalid;q=oops", "en"},
	} {
		if got := shortLinkLanguage(tc.header); got != tc.want {
			t.Errorf("%q: got %q want %q", tc.header, got, tc.want)
		}
	}
}

func TestAcceptsGzip(t *testing.T) {
	for _, tc := range []struct {
		values []string
		want   bool
	}{
		{nil, false}, {[]string{"gzip"}, true}, {[]string{"br, GZIP;q=0.5"}, true},
		{[]string{"gzip;q=0"}, false}, {[]string{"*;q=1, gzip;q=0"}, false},
		{[]string{"gzip;q=0", "*;q=1"}, false}, {[]string{"*;q=0.5"}, true},
		{[]string{"gzip;q=invalid"}, false}, {[]string{"gzip;q=2"}, false},
	} {
		if got := acceptsGzip(tc.values); got != tc.want {
			t.Errorf("%q: got %v", tc.values, got)
		}
	}
}
