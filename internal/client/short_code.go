package client

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var codePath = regexp.MustCompile(`/c/[A-Z0-9]{6}$`)

// ShortCodeURL validates an address and supplies HTTPS when the scheme is omitted.
func ShortCodeURL(value string) (*url.URL, bool) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || !codePath.MatchString(u.Path) {
		return nil, false
	}
	return u, true
}

// ConnectionAddress validates either a token or a short-code address.
func ConnectionAddress(value string) (string, string, error) {
	if u, ok := ShortCodeURL(value); ok {
		u.Path = codePath.ReplaceAllString(u.Path, "/client")
		if u.Scheme == "https" {
			u.Scheme = "wss"
		} else {
			u.Scheme = "ws"
		}
		return u.String(), "", nil
	}
	t, err := protocol.Decode(value)
	if err != nil {
		return "", "", TokenValidationError()
	}
	return t.URL, t.Identifier, nil
}

func redeemShortCode(ctx context.Context, u *url.URL) (protocol.Token, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return protocol.Token{}, "", err
	}
	req.Header.Set(version.Header, version.Current)
	hc := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := hc.Do(req)
	if err != nil {
		return protocol.Token{}, "", err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUpgradeRequired || res.StatusCode == http.StatusOK {
		if err := version.Check(version.Current, res.Header.Get(version.Header)); err != nil {
			return protocol.Token{}, "", err
		}
	}
	if res.StatusCode == http.StatusUnauthorized {
		return protocol.Token{}, "", i18n.NewError("invalid.or.expired.token")
	}
	if res.StatusCode != http.StatusOK {
		return protocol.Token{}, "", fmt.Errorf("short code lookup failed (HTTP %d)", res.StatusCode)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&result); err != nil {
		return protocol.Token{}, "", err
	}
	token, err := protocol.Decode(result.Token)
	if err != nil {
		return protocol.Token{}, "", err
	}
	return token, result.Token, nil
}
