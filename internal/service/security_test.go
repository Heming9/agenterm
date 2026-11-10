package service

import (
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestShortCodeLimiterIsIndependent(t *testing.T) {
	s := &runtimeState{enableShortCode: true, noPassword: true, clientURL: "wss://example.com/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	code := issued["short_code"].(string)
	decoded, err := protocol.Decode(issued["client_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	tokenPath := "/c/" + decoded.Secret
	request := func(method, path string) int {
		var body *strings.Reader
		if path == "/mcp" {
			body = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		} else {
			body = strings.NewReader("")
		}
		r := httptest.NewRequest(method, path, body)
		if method == http.MethodPost && path == "/mcp" {
			setMCPHeaders(r)
		}
		w := httptest.NewRecorder()
		if path == "/mcp" {
			s.mcp(w, r)
		} else if strings.HasPrefix(path, "/c/") {
			if path != tokenPath {
				r.Header.Set("Accept", "text/html")
			}
			s.installWrapper("v1.2.3").ServeHTTP(w, r)
		} else {
			s.testFrontend().shortLink(w, r)
		}
		if path == tokenPath && (w.Code != http.StatusOK || !strings.Contains(w.Body.String(), issued["client_token"].(string))) {
			t.Fatalf("valid token installer: %d", w.Code)
		}
		return w.Code
	}
	// Token secret lookups do not count against the short-code limit.
	for i := 0; i < 10; i++ {
		if got := request(http.MethodGet, tokenPath); got != http.StatusOK {
			t.Fatalf("token lookup %d: %d", i, got)
		}
	}
	for i := 0; i < failureLimit; i++ {
		if got := request(http.MethodGet, "/c/aaaaaa"); got != http.StatusUnauthorized {
			t.Fatalf("short lookup %d: %d", i, got)
		}
	}
	if got := request(http.MethodGet, "/c/"+code); got != http.StatusTooManyRequests {
		t.Fatalf("short link not limited: %d", got)
	}
	if got := request(http.MethodGet, tokenPath); got != http.StatusOK {
		t.Fatalf("token lookup limited: %d", got)
	}
	if got := request(http.MethodPost, "/mcp"); got != http.StatusOK {
		t.Fatalf("management authentication limited: %d", got)
	}
	if got := request(http.MethodGet, "/c/"+code); got != http.StatusTooManyRequests {
		t.Fatalf("other auth reset short-code limit: %d", got)
	}
}

func TestFailureLimiterExpiry(t *testing.T) {
	var l authLimiter
	now := time.Unix(1000, 0)
	for i := 0; i < failureLimit; i++ {
		if wait := l.checkWithReset("ip", false, true, now); wait != 0 {
			t.Fatal("blocked too early")
		}
	}
	if wait := l.checkWithReset("ip", true, true, now.Add(time.Second)); wait != time.Second {
		t.Fatalf("remaining wait: %s", wait)
	}
	if wait := l.checkWithReset("ip", true, true, now.Add(2*time.Second)); wait != 0 {
		t.Fatal("block did not expire")
	}
	if _, ok := l.entries["ip"]; ok {
		t.Fatal("success did not clear failures")
	}
}

func TestAuthenticationRequiresConfiguredIPHeaderAcrossEndpoints(t *testing.T) {
	for _, endpoint := range []string{"mcp", "client", "probe", "redeem", "installer"} {
		for _, header := range []struct {
			name   string
			values []string
		}{
			{"missing", nil},
			{"empty", []string{""}},
			{"multiple", []string{"192.0.2.1", "192.0.2.2"}},
			{"comma-separated", []string{"192.0.2.1, 192.0.2.2"}},
			{"invalid", []string{"invalid"}},
			{"with-port", []string{"192.0.2.1:1234"}},
			{"scoped-ipv6", []string{"fe80::1%lo0"}},
		} {
			t.Run(endpoint+"/"+header.name, func(t *testing.T) {
				s := &runtimeState{noPassword: true, clientIPHeader: "X-Client-IP", enableShortCode: true}
				r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
				setMCPHeaders(r)
				r.RemoteAddr = "203.0.113.5:1234"
				r.Header.Set("X-Forwarded-For", "192.0.2.1")
				r.Header.Set("X-Real-IP", "192.0.2.1")
				for _, value := range header.values {
					r.Header.Add("X-Client-IP", value)
				}
				w := httptest.NewRecorder()
				switch endpoint {
				case "mcp":
					s.mcp(w, r)
				case "client":
					s.connect(w, r)
				case "probe":
					r.URL.RawQuery = "probe=1"
					s.connect(w, r)
				case "redeem":
					s.redeemInvitation(w, r)
				case "installer":
					r.Method, r.URL.Path = http.MethodGet, "/c/AAAAAA"
					s.installWrapper("v1.2.3").ServeHTTP(w, r)
				}
				if w.Code != http.StatusBadRequest {
					t.Fatalf("expected invalid real IP rejection, got %d: %s", w.Code, w.Body.String())
				}
				if len(s.limiter.entries) != 0 {
					t.Fatal("invalid IP request polluted the authentication limiter")
				}
			})
		}
	}
}

func TestShortCodeLimiterNormalizesAndUsesConfiguredIP(t *testing.T) {
	for _, configured := range []bool{false, true} {
		s := &runtimeState{}
		if configured {
			s.clientIPHeader = "X-Real-IP"
		}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "[::ffff:192.0.2.1]:1234"
		req.Header.Set("X-Real-IP", "::ffff:192.0.2.1")
		for i := 0; i < failureLimit; i++ {
			if status, _ := s.authorizeShortCode(req, false, false); status != http.StatusUnauthorized {
				t.Fatalf("failure %d: %d", i, status)
			}
		}
		req.RemoteAddr = "192.0.2.1:9999"
		req.Header.Set("X-Real-IP", "192.0.2.1")
		if status, _ := s.authorizeShortCode(req, true, false); status != http.StatusTooManyRequests {
			t.Fatalf("normalized IP bypassed limit: %d", status)
		}
		req.RemoteAddr = "192.0.2.2:1234"
		req.Header.Set("X-Real-IP", "192.0.2.2")
		if status, _ := s.authorizeShortCode(req, true, false); status != 0 {
			t.Fatalf("other IP blocked: %d", status)
		}
	}
}

func TestRequestIPUsesConfiguredSource(t *testing.T) {
	for _, tc := range []struct {
		name, header, remote, value, want string
		invalid                           bool
	}{
		{"peer-ipv4", "", "192.0.2.1:1234", "invalid", "192.0.2.1", false},
		{"peer-ipv6", "", "[2001:db8::1]:1234", "invalid", "2001:db8::1", false},
		{"peer-mapped-ipv4", "", "[::ffff:192.0.2.1]:1234", "invalid", "192.0.2.1", false},
		{"invalid-peer", "", "invalid", "203.0.113.1", "", true},
		{"custom-header", "CF-Connecting-IP", "192.0.2.1:1234", "203.0.113.1", "203.0.113.1", false},
		{"custom-missing", "CF-Connecting-IP", "192.0.2.1:1234", "", "", true},
		{"custom-invalid", "CF-Connecting-IP", "192.0.2.1:1234", "invalid", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &runtimeState{clientIPHeader: tc.header}
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			setMCPHeaders(r)
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Real-IP", "198.51.100.1")
			r.Header.Set("X-Forwarded-For", "198.51.100.2")
			if tc.value != "" {
				r.Header.Set("CF-Connecting-IP", tc.value)
			}
			got, err := s.requestIP(r)
			if got != tc.want || (err != nil) != tc.invalid {
				t.Fatalf("got %q, %v; want %q, invalid=%v", got, err, tc.want, tc.invalid)
			}
		})
	}
}

func TestProgressiveFailurePolicy(t *testing.T) {
	var l authLimiter
	now := time.Unix(1000, 0)
	for i := 0; i < 2; i++ {
		if wait := l.checkWithReset("ip", false, true, now); wait != 0 {
			t.Fatal("early block")
		}
	}
	for step := 1; step <= 302; step++ {
		if wait := l.checkWithReset("ip", false, true, now); wait != 0 {
			t.Fatalf("step %d blocked: %s", step, wait)
		}
		expected := time.Duration(step) * 2 * time.Second
		if expected > maxFailureBlock {
			expected = maxFailureBlock
		}
		if wait := l.checkWithReset("ip", false, true, now); wait != expected {
			t.Fatalf("step %d wait %s want %s", step, wait, expected)
		}
		now = now.Add(expected)
	}
	if wait := l.checkWithReset("ip", true, true, now); wait != 0 {
		t.Fatal("valid request blocked after wait")
	}
	if wait := l.checkWithReset("ip", false, true, now); wait != 0 || l.entries["ip"].failures != 1 {
		t.Fatal("success did not reset count")
	}
}

func TestProgressiveLimiterConcurrentFailures(t *testing.T) {
	var l authLimiter
	var accepted atomic.Int32
	var wg sync.WaitGroup
	now := time.Unix(1000, 0)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.checkWithReset("ip", false, true, now) == 0 {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := accepted.Load(); got != failureLimit {
		t.Fatalf("accepted %d concurrent attempts", got)
	}
	if got := l.entries["ip"].failures; got != failureLimit {
		t.Fatalf("counted %d failures", got)
	}
}

func TestAdminAndShortCodeLimitsAreSeparate(t *testing.T) {
	s := &runtimeState{enableShortCode: true, adminKey: "strong-admin-test", clientURL: "wss://example.com/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	auth := func(token string) int {
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		setMCPHeaders(r)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.mcp(w, r)
		return w.Code
	}
	for i := 0; i < failureLimit; i++ {
		if got := auth("invalid-admin"); got != http.StatusUnauthorized {
			t.Fatalf("admin guess %d: %d", i, got)
		}
	}
	if got := auth("strong-admin-test"); got != http.StatusTooManyRequests {
		t.Fatalf("admin not blocked: %d", got)
	}
	if got := auth(issued["control_token"].(string)); got != http.StatusOK {
		t.Fatalf("scoped token blocked: %d", got)
	}
	for i := 0; i < 8; i++ {
		if got := auth("Agenterm_invalid:invalid"); got != http.StatusUnauthorized {
			t.Fatalf("scoped failure %d: %d", i, got)
		}
	}
	w := httptest.NewRecorder()
	s.testFrontend().shortLink(w, httptest.NewRequest(http.MethodGet, "/c/"+issued["short_code"].(string), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("short code blocked by admin guesses: %d", w.Code)
	}
	if got := auth("strong-admin-test"); got != http.StatusTooManyRequests {
		t.Fatalf("other auth reset admin limit: %d", got)
	}
}

func TestShortCodeViewingCannotResetFailures(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	code := issued["short_code"].(string)
	invalid := "aaaaaa"
	if code == invalid {
		invalid = "bbbbbb"
	}
	view := func(value string) int {
		w := httptest.NewRecorder()
		s.testFrontend().shortLink(w, httptest.NewRequest(http.MethodGet, "/c/"+value, nil))
		return w.Code
	}
	for i := 0; i < 2; i++ {
		if got := view(invalid); got != http.StatusUnauthorized {
			t.Fatalf("invalid: %d", got)
		}
	}
	for i := 0; i < 2; i++ {
		if got := view(code); got != http.StatusOK {
			t.Fatalf("valid view: %d", got)
		}
	}
	if got := view(invalid); got != http.StatusUnauthorized {
		t.Fatalf("third failure: %d", got)
	}
	if got := view(code); got != http.StatusTooManyRequests {
		t.Fatalf("view reset failures: %d", got)
	}
	s.limiter.mu.Lock()
	e := s.limiter.entries["192.0.2.1"]
	e.blockedUntil = time.Now().Add(-time.Second)
	s.limiter.entries["192.0.2.1"] = e
	s.limiter.mu.Unlock()
	req := httptest.NewRequest(http.MethodPost, "/c/"+code, nil)
	req.Header.Set(version.Header, version.Current)
	w := httptest.NewRecorder()
	s.installWrapper("v1.2.3").ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("redemption: %d", w.Code)
	}
	s.limiter.mu.Lock()
	_, exists := s.limiter.entries["192.0.2.1"]
	s.limiter.mu.Unlock()
	if exists {
		t.Fatal("successful redemption did not reset failures")
	}
}

func TestLimiterCapacityRecoversAtEarliestOpportunity(t *testing.T) {
	now := time.Unix(1000, 0)
	var l authLimiter
	l.entries = make(map[string]failureEntry, maxFailureEntries)
	for i := 0; i < maxFailureEntries; i++ {
		l.entries[fmt.Sprint(i)] = failureEntry{failures: 3, lastFailure: now, blockedUntil: now.Add(10 * time.Minute)}
	}
	oldest := l.entries["42"]
	oldest.blockedUntil = now.Add(5 * time.Second)
	l.entries["42"] = oldest
	if got := l.checkWithReset("new", false, true, now); got != 5*time.Second {
		t.Fatalf("wait %s", got)
	}
	if got := l.checkWithReset("new", false, true, now.Add(5*time.Second)); got != 0 {
		t.Fatalf("retry wait %s", got)
	}
	if _, ok := l.entries["42"]; ok {
		t.Fatal("old unblocked entry not evicted")
	}
	if _, ok := l.entries["new"]; !ok || len(l.entries) != maxFailureEntries {
		t.Fatal("new entry not retained at capacity")
	}
}

func TestConcurrentShortCodeRedemptionLimit(t *testing.T) {
	s := &runtimeState{enableShortCode: true, clientURL: "wss://example.com/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	code := issued["short_code"].(string)
	var successes, unexpected atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < maxShortCodeRedemptions+8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodPost, "/c/"+code, nil)
			r.Header.Set(version.Header, version.Current)
			w := httptest.NewRecorder()
			s.installWrapper("v1.2.3").ServeHTTP(w, r)
			switch w.Code {
			case http.StatusOK:
				successes.Add(1)
			case http.StatusUnauthorized, http.StatusTooManyRequests:
			default:
				unexpected.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != maxShortCodeRedemptions || unexpected.Load() != 0 {
		t.Fatalf("successes=%d unexpected=%d", successes.Load(), unexpected.Load())
	}
}

func TestAdminKeyWithTokenPrefixLimitsAmbiguousFailures(t *testing.T) {
	s := &runtimeState{adminKey: "Agenterm_custom_admin", clientURL: "wss://example.com/client"}
	issued, err := s.createClient("")
	if err != nil {
		t.Fatal(err)
	}
	auth := func(value string) int {
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		setMCPHeaders(r)
		r.Header.Set("Authorization", "Bearer "+value)
		w := httptest.NewRecorder()
		s.mcp(w, r)
		return w.Code
	}
	for i := 0; i < failureLimit; i++ {
		if got := auth("Agenterm_wrong:wrong"); got != http.StatusUnauthorized {
			t.Fatalf("guess %d: %d", i, got)
		}
	}
	if got := auth("Agenterm_custom_admin"); got != http.StatusTooManyRequests {
		t.Fatalf("admin not blocked: %d", got)
	}
	if got := auth(issued["control_token"].(string)); got != http.StatusOK {
		t.Fatalf("valid control token blocked: %d", got)
	}
}
