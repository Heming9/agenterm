package main

import (
	"net/url"
	"testing"
)

func TestParseDeepLink(t *testing.T) {
	address := "https://example.com/agenterm/c/Ab1234"
	valid := "agenterm://connect?" + url.Values{"url": {address}}.Encode()
	if got, err := parseDeepLink(valid); err != nil || got != address {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{
		"https://connect?url=" + url.QueryEscape(address),
		"agenterm://other?url=" + url.QueryEscape(address),
		"agenterm://connect/path?url=" + url.QueryEscape(address),
		"agenterm://connect?url=" + url.QueryEscape(address) + "&url=" + url.QueryEscape(address),
		"agenterm://connect?url=" + url.QueryEscape("example.com/c/Ab1234"),
		"agenterm://connect?url=" + url.QueryEscape("https://example.com/c/Ab1234?secret=x"),
		"agenterm://connect?url=" + url.QueryEscape("https://user@example.com/c/Ab1234"),
	} {
		if _, err := parseDeepLink(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
