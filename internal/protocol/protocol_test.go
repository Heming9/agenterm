package protocol

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestCompactTokenRoundTrip(t *testing.T) {
	secret := base64.RawURLEncoding.EncodeToString([]byte("0123456789"))
	for _, address := range []string{
		"ws://127.0.0.1:8080/agenterm/client",
		"ws://192.168.100.200:65535/agenterm/client",
		"wss://192.168.100.200/agenterm/client",
		"wss://example.com/agenterm/client",
		"wss://example.com:8443/agenterm/client",
		"ws://[::1]:8080/agenterm/client",
		"wss://[2001:db8::1]:443/agenterm/client",
		"wss://[::1]/agenterm/client",
		"ws://[fe80::1%25en0]:8080/agenterm/client",
		"wss://example.com/a/client",
		"ws://127.0.0.1:8080/tools/remote/client",
		"wss://[::1]/a/client",
		"ws://[fe80::1%25en0]:8080/a/client",
		"wss://example.com/client",
		"ws://127.0.0.1:8080/client",
	} {
		t.Run(address, func(t *testing.T) {
			want := Token{URL: address, Secret: secret}
			encoded, err := Encode(want)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(encoded, "agenterm:") {
				t.Fatal("token must start with agenterm:")
			}
			if address == "ws://127.0.0.1:8080/agenterm/client" && len(encoded) != 32 {
				t.Fatalf("default token length = %d, want 32", len(encoded))
			}
			got, err := Decode(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("decoded token = %#v, want %#v", got, want)
			}
		})
	}
}

func TestOptionalPortEncoding(t *testing.T) {
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, TokenSecretSize))
	for _, tc := range []struct {
		address string
		flags   byte
		suffix  string
	}{
		{"wss://example.com/agenterm/client", tokenFormat | tokenPackedHost | 2, "\x15\x80\x4d\x40\xc1\x66\x0c\xf3\x40"},
		{"wss://example.com:8080/agenterm/client", tokenFormat | tokenPackedHost | tokenHasPort | 2, "\x1f\x90\x15\x80\x4d\x40\xc1\x66\x0c\xf3\x40"},
		{"ws://127.0.0.1/agenterm/client", tokenFormat | tokenIPv4 | 1, "\x7f\x00\x00\x01"},
		{"ws://127.0.0.1:8080/agenterm/client", tokenFormat | tokenIPv4 | tokenHasPort | 1, "\x1f\x90\x7f\x00\x00\x01"},
	} {
		encoded, err := Encode(Token{URL: tc.address, Secret: secret})
		if err != nil {
			t.Fatal(err)
		}
		payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, tokenPrefix))
		if err != nil || payload[0] != tc.flags || string(payload[1+TokenSecretSize:]) != tc.suffix {
			t.Fatalf("unexpected payload for %s: %x, %v", tc.address, payload, err)
		}
	}
}

func TestPackedHostnames(t *testing.T) {
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, TokenSecretSize))
	for _, tc := range []struct {
		input, want string
		length      int
		packed      bool
	}{
		{"wss://agenterm.net/client", "wss://agenterm.net/client", 39, true},
		{"wss://例子.中国/client", "wss://xn--fsqu00a.xn--fiqs8s/client", 49, true},
		{"wss://Example.com/client", "wss://Example.com/client", 0, false},
		{"ws://[fe80::1%25en0]:8080/client", "ws://[fe80::1%25en0]:8080/client", 0, false},
	} {
		encoded, err := Encode(Token{URL: tc.input, Secret: secret})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, tokenPrefix))
		if (data[0]&tokenPackedHost != 0) != tc.packed {
			t.Fatalf("unexpected encoding for %s", tc.input)
		}
		if tc.length != 0 && len(encoded) != tc.length {
			t.Fatalf("%s: length %d, want %d", tc.input, len(encoded), tc.length)
		}
		decoded, err := Decode(encoded)
		if err != nil || decoded.URL != tc.want {
			t.Fatalf("%s: got %#v, %v", tc.input, decoded, err)
		}
	}
	for _, host := range []string{"a", "ab", "abc", "abcd", "abcde", hostnameAlphabet} {
		packed, ok := packHostname(host)
		got, valid := unpackHostname(packed)
		if !ok || !valid || got != host {
			t.Fatalf("host %q: got %q", host, got)
		}
	}
	for _, invalid := range [][]byte{{0}, {0xff}, {0x05}, {0x04, 0x00}} {
		if _, ok := unpackHostname(invalid); ok {
			t.Fatalf("accepted malformed packed host %x", invalid)
		}
	}
}

func TestDecodeRejectsInvalidAndLegacyTokens(t *testing.T) {
	wrap := func(scheme byte, host string) string {
		payload := append([]byte{scheme}, make([]byte, TokenSecretSize)...)
		return "agenterm:" + base64.RawURLEncoding.EncodeToString(append(payload, host...))
	}
	legacy := base64.RawURLEncoding.EncodeToString([]byte(`{"url":"ws://localhost:8080/client","secret":"test-secret"}`))
	for name, token := range map[string]string{
		"old root routes":    wrap(1, "localhost"),
		"legacy":             legacy,
		"prefixed legacy":    "agenterm:" + legacy,
		"missing prefix":     strings.TrimPrefix(wrap(tokenFormat|1, "localhost:8080"), "agenterm:"),
		"empty payload":      "agenterm:",
		"invalid base64":     "agenterm:!",
		"truncated secret":   "agenterm:" + base64.RawURLEncoding.EncodeToString([]byte{1, 2, 3}),
		"missing host":       wrap(tokenFormat|1, ""),
		"unsupported format": wrap(tokenFormat|3, "localhost"),
		"host with path":     wrap(tokenFormat|1, "localhost/extra"),
		"host with query":    wrap(tokenFormat|1, "localhost?query"),
		"host with user":     wrap(tokenFormat|1, "user@localhost"),
		"host with no name":  wrap(tokenFormat|1, ":8080"),
		"truncated IPv4":     wrap(tokenFormat|5, "abc"),
		"truncated IPv6":     wrap(tokenFormat|9, "short"),
		"extra IPv4 bytes":   wrap(tokenFormat|5, strings.Repeat("a", 5)),
		"extra IPv6 bytes":   wrap(tokenFormat|9, strings.Repeat("a", 17)),
		"missing port":       wrap(tokenFormat|tokenHasPort|1, "x"),
		"zero port":          wrap(tokenFormat|tokenHasPort|1, "\x00\x00localhost"),
		"unknown flags":      wrap(tokenFormat|tokenPackedHost|tokenIPv4|1, "localhost"),
		"missing path size":  wrap(tokenFormat|tokenCustomPath|1, "x"),
		"truncated path":     wrap(tokenFormat|tokenCustomPath|1, "\x00\x10/a"),
		"unclean path":       wrap(tokenFormat|tokenCustomPath|1, "\x00\x04/a/.localhost"),
		"relative path":      wrap(tokenFormat|tokenCustomPath|1, "\x00\x01alocalhost"),
		"noncanonical root":  wrap(tokenFormat|tokenCustomPath|1, "\x00\x01/localhost"),
		"conflicting flags":  wrap(tokenFormat|13, "localhost"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(token); err == nil {
				t.Fatal("invalid token was accepted")
			}
		})
	}
}

func TestEncodeRejectsInvalidFields(t *testing.T) {
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, TokenSecretSize))
	for _, token := range []Token{
		{URL: "ws://localhost//client", Secret: secret},
		{URL: "ws://localhost/a/../client", Secret: secret},
		{URL: "ws://localhost/%61/client", Secret: secret},
		{URL: "http://localhost/agenterm/client", Secret: secret},
		{URL: "ws://localhost/other", Secret: secret},
		{URL: "ws://localhost/agenterm/client?query", Secret: secret},
		{URL: "ws://localhost/agenterm/client#fragment", Secret: secret},
		{URL: "ws://user@localhost/agenterm/client", Secret: secret},
		{URL: "ws://localhost/agenterm/client", Secret: "short"},
		{URL: "ws://localhost/agenterm/client", Secret: strings.Repeat("!", 32)},
		{URL: "ws://127.0.0.1:65536/agenterm/client", Secret: secret},
		{URL: "ws://127.0.0.1:0/agenterm/client", Secret: secret},
		{URL: "ws://example.com:65536/agenterm/client", Secret: secret},
		{URL: "ws://example.com:0/agenterm/client", Secret: secret},
	} {
		if _, err := Encode(token); err == nil {
			t.Errorf("invalid token fields were accepted: %#v", token)
		}
	}
}

func TestNormalizePathPrefix(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"", ""}, {"/", ""}, {"/agenterm", "/agenterm"}, {"/a/", "/a"},
		{"/tools/remote", "/tools/remote"}, {"/v1.0/A_~-9/", "/v1.0/A_~-9"},
	} {
		got, err := NormalizePathPrefix(tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("normalize %q: got %q, %v; want %q", tc.raw, got, err, tc.want)
		}
	}
	for _, raw := range []string{"a", "//", "/a//b", "/a//", "/.", "/a/..", "/a?b", "/a#b", "/a%b", "/a b", "/a\\b", "/中文", "/" + strings.Repeat("a", 1024)} {
		if _, err := NormalizePathPrefix(raw); err == nil {
			t.Errorf("invalid prefix accepted: %q", raw)
		}
	}
}
