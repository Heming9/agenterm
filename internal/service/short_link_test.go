package service

import (
	"html"
	"io"
	"net/http"
	"net/http/httptest"
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
			for i := 0; i < 2; i++ {
				res, err := browser(http.MethodGet, link)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(res.Body)
				res.Body.Close()
				text := html.UnescapeString(string(body))
				if res.StatusCode != http.StatusOK || !strings.Contains(text, issued["client_token"].(string)) || !strings.Contains(text, issued["windows_powershell_command"].(string)) || !strings.Contains(text, issued["macos_linux_command"].(string)) || strings.Contains(text, issued["control_token"].(string)) {
					t.Fatalf("page status %d: %s", res.StatusCode, body)
				}
				expires, err := time.Parse(time.RFC3339, issued["token_initial_expires_at"].(string))
				if err != nil {
					t.Fatal(err)
				}
				expected := expires.In(time.FixedZone("北京时间", 8*60*60)).Format("2006-01-02 15:04:05") + "（北京时间）"
				if !strings.Contains(text, "连接方法") || !strings.Contains(text, "有效期") || !strings.Contains(text, expected) || strings.Contains(text, "短码最多") {
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
		h.s.testFrontend().shortLink(w, httptest.NewRequest(http.MethodGet, "/c/"+code, nil))
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

func TestShortCodeAddressOmitsScheme(t *testing.T) {
	for _, tc := range []struct{ clientURL, want string }{
		{"wss://agenterm.net/client", "agenterm.net/c/abcdef"},
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
