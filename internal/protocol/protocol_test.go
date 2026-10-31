package protocol

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestCompactTokenRoundTrip(t *testing.T) {
	secret := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef"))
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
			if strings.HasPrefix(address, "ws://127.0.0.1:8080/") && len(encoded) != 40 {
				t.Fatalf("default token length = %d, want 40", len(encoded))
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
		"truncated IPv4":     wrap(tokenFormat|5, "short"),
		"truncated IPv6":     wrap(tokenFormat|9, "short"),
		"extra IPv4 bytes":   wrap(tokenFormat|5, strings.Repeat("a", 7)),
		"extra IPv6 bytes":   wrap(tokenFormat|9, strings.Repeat("a", 19)),
		"unknown flags":      wrap(tokenFormat|33, "localhost"),
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
		{URL: "ws://localhost/client", Secret: secret},
		{URL: "http://localhost/agenterm/client", Secret: secret},
		{URL: "ws://localhost/other", Secret: secret},
		{URL: "ws://localhost/agenterm/client?query", Secret: secret},
		{URL: "ws://localhost/agenterm/client#fragment", Secret: secret},
		{URL: "ws://user@localhost/agenterm/client", Secret: secret},
		{URL: "ws://localhost/agenterm/client", Secret: "short"},
		{URL: "ws://localhost/agenterm/client", Secret: strings.Repeat("!", 32)},
		{URL: "ws://127.0.0.1:65536/agenterm/client", Secret: secret},
		{URL: "ws://127.0.0.1:0/agenterm/client", Secret: secret},
	} {
		if _, err := Encode(token); err == nil {
			t.Errorf("invalid token fields were accepted: %#v", token)
		}
	}
}
