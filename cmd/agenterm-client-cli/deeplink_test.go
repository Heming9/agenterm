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
	for _, mode := range []string{"temporary", "persistent"} {
		for _, path := range []string{"", "/"} {
			value := "agenterm-cli://connect" + path + "?" + url.Values{"url": {address}, "mode": {mode}}.Encode()
			got, err := deepLinkArgs([]string{value})
			if err != nil || len(got) != 4 || got[0] != "-token" || got[1] != address || got[2] != "-connection-mode" || got[3] != mode {
				t.Fatalf("mode %s path %q: %v %v", mode, path, got, err)
			}
		}
	}
	for _, bad := range []string{"agenterm-cli://connect?url=https%3A%2F%2Fexample.com%2Fc%2Fab1234&extra=x", "agenterm-cli://connect?url=file%3A%2F%2Fetc%2Fpasswd", "agenterm-cli://connect?url=" + url.QueryEscape(address) + "&mode=other", "agenterm-cli://connect?url=" + url.QueryEscape(address) + "&mode=temporary&mode=persistent"} {
		if _, err := deepLinkArgs([]string{bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
