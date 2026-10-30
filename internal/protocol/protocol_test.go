package protocol

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestCompactTokenRoundTrip(t *testing.T) {
	secret := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef"))
	for _, address := range []string{
		"ws://127.0.0.1:8080/client",
		"ws://192.168.100.200:65535/client",
		"wss://192.168.100.200/client",
		"wss://example.com/client",
		"wss://example.com:8443/client",
		"ws://[::1]:8080/client",
		"wss://[2001:db8::1]:443/client",
		"wss://[::1]/client",
		"ws://[fe80::1%25en0]:8080/client",
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
			if address == "ws://127.0.0.1:8080/client" && len(encoded) != 40 {
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
		"legacy":             legacy,
		"prefixed legacy":    "agenterm:" + legacy,
		"missing prefix":     strings.TrimPrefix(wrap(1, "localhost:8080"), "agenterm:"),
		"empty payload":      "agenterm:",
		"invalid base64":     "agenterm:!",
		"truncated secret":   "agenterm:" + base64.RawURLEncoding.EncodeToString([]byte{1, 2, 3}),
		"missing host":       wrap(1, ""),
		"unsupported format": wrap(3, "localhost"),
		"host with path":     wrap(1, "localhost/extra"),
		"host with query":    wrap(1, "localhost?query"),
		"host with user":     wrap(1, "user@localhost"),
		"host with no name":  wrap(1, ":8080"),
		"truncated IPv4":     wrap(5, "short"),
		"truncated IPv6":     wrap(9, "short"),
		"extra IPv4 bytes":   wrap(5, strings.Repeat("a", 7)),
		"extra IPv6 bytes":   wrap(9, strings.Repeat("a", 19)),
		"unknown flags":      wrap(17, "localhost"),
		"conflicting flags":  wrap(13, "localhost"),
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
		{URL: "http://localhost/client", Secret: secret},
		{URL: "ws://localhost/other", Secret: secret},
		{URL: "ws://localhost/client?query", Secret: secret},
		{URL: "ws://localhost/client#fragment", Secret: secret},
		{URL: "ws://user@localhost/client", Secret: secret},
		{URL: "ws://localhost/client", Secret: "short"},
		{URL: "ws://localhost/client", Secret: strings.Repeat("!", 32)},
		{URL: "ws://127.0.0.1:65536/client", Secret: secret},
		{URL: "ws://127.0.0.1:0/client", Secret: secret},
	} {
		if _, err := Encode(token); err == nil {
			t.Errorf("invalid token fields were accepted: %#v", token)
		}
	}
}
