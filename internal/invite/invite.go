// Package invite exchanges a short-lived invitation for a connection token.
package invite

import (
	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
	"agenterm/internal/version"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func Endpoint(value string) (string, string, error) {
	t, err := protocol.Decode(strings.TrimSpace(value))
	if err != nil {
		return "", "", err
	}
	u, err := url.Parse(t.URL)
	if err != nil {
		return "", "", err
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	} else {
		u.Scheme = "http"
	}
	u.Path = protocol.PathPrefix + "/invite/redeem"
	return u.String(), t.Secret, nil
}

func Redeem(ctx context.Context, value string) (string, error) {
	endpoint, secret, err := Endpoint(value)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set(version.Header, version.Current)
	hc := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := hc.Do(req)
	if err != nil {
		return "", i18n.NewError("redeem.invitation.w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUpgradeRequired || res.StatusCode == http.StatusOK {
		if err := version.Check(version.Current, res.Header.Get(version.Header)); err != nil {
			return "", err
		}
	}
	if res.StatusCode != http.StatusOK {
		return "", i18n.NewError("invitation.invalid.expired.or.already.used.http.d", res.StatusCode)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&result); err != nil {
		return "", err
	}
	decoded, err := protocol.Decode(result.Token)
	if err != nil {
		return "", err
	}
	source, _ := protocol.Decode(value)
	if decoded.URL != source.URL {
		return "", i18n.NewError("invitation.response.points.to.a.different.server")
	}
	return result.Token, nil
}
