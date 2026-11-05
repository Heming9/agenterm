package main

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestAuthorizationChoiceRedirectedInput(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int
	}{{"", 0}, {"1\n", 1}, {"2\n", 2}, {"3\n", 0}, {"y\n", 0}} {
		if got := authorizationChoice(bufio.NewReader(strings.NewReader(tc.input)), io.Discard); got != tc.want {
			t.Errorf("%q: got %d, want %d", tc.input, got, tc.want)
		}
	}
}
