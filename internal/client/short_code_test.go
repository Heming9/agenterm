package client

import "testing"

func TestShortCodeURL(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		valid       bool
	}{
		{"example.com/agenterm/c/AB0123", "https://example.com/agenterm/c/AB0123", true},
		{"http://127.0.0.1:8080/c/000000", "http://127.0.0.1:8080/c/000000", true},
		{"https://u:p@example.com/c/ABCDEF", "", false},
		{"https://example.com/c/abcdef", "https://example.com/c/abcdef", true},
		{"https://example.com/c/ABCDEF?x=1", "", false},
		{"https://example.com/c/ABCDEF/extra", "", false},
	} {
		u, ok := ShortCodeURL(tc.input)
		if ok != tc.valid || (ok && u.String() != tc.want) {
			t.Errorf("%q: %v %v", tc.input, u, ok)
		}
	}
}
