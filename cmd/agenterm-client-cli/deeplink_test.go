package main

import (
	"net/url"
	"testing"
)

func TestDeepLinkArgs(t *testing.T) {
	address := "https://example.com/base/c/ab1234"
	link := "agenterm-cli://connect?" + url.Values{"url": {address}}.Encode()
	got, err := deepLinkArgs([]string{link})
	if err != nil || len(got) != 2 || got[0] != "-token" || got[1] != address {
		t.Fatalf("got %v: %v", got, err)
	}
	for _, bad := range []string{"agenterm-cli://connect?url=https%3A%2F%2Fexample.com%2Fc%2Fab1234&extra=x", "agenterm-cli://connect?url=file%3A%2F%2Fetc%2Fpasswd"} {
		if _, err := deepLinkArgs([]string{bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
