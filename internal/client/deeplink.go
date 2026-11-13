package client

import (
	"errors"
	"net/url"
	"strings"
)

// ParseDeepLink accepts only a full short-code address, never a command or token.
func ParseDeepLink(raw, scheme string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != scheme || u.Host != "connect" || u.User != nil || u.Path != "" || u.Fragment != "" {
		return "", errors.New("invalid Agenterm link")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 || len(q["url"]) != 1 {
		return "", errors.New("invalid Agenterm link")
	}
	address := q.Get("url")
	if !strings.Contains(address, "://") {
		return "", errors.New("invalid Agenterm link")
	}
	if _, ok := ShortCodeURL(address); !ok {
		return "", errors.New("invalid Agenterm link")
	}
	return address, nil
}
