package client

import (
	"errors"
	"net/url"
	"strings"
)

// ParseDeepLink accepts only a short-code address and an optional connection mode.
func ParseDeepLink(raw, scheme string) (string, string, error) {
	invalid := func() (string, string, error) { return "", "", errors.New("invalid Agenterm link") }
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != scheme || u.Host != "connect" || u.User != nil || u.Path != "" || u.Fragment != "" {
		return invalid()
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["url"]) != 1 || len(q) < 1 || len(q) > 2 {
		return invalid()
	}
	for key := range q {
		if key != "url" && key != "mode" {
			return invalid()
		}
	}
	mode := ""
	if values, ok := q["mode"]; ok {
		if len(values) != 1 || (values[0] != ConnectionModeTemporary && values[0] != ConnectionModePersistent) {
			return invalid()
		}
		mode = values[0]
	}
	address := q.Get("url")
	if !strings.Contains(address, "://") {
		return invalid()
	}
	if _, ok := ShortCodeURL(address); !ok {
		return invalid()
	}
	return address, mode, nil
}
