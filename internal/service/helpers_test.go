package service

import (
	"agenterm/internal/version"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"
	"testing"
)

// Convenience entry points for fixtures; production uses the control API.
// createClient issues a single-device credential and a single-use invitation.
func (s *runtimeState) createClient(owner string) (map[string]any, error) {
	return s.createClientMode(owner, createClientArgs{TimeoutSeconds: defaultInvitationTimeoutSeconds})
}

func (s *runtimeState) listClients(secret string) []clientInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listClientsLocked(secret)
}

func deviceTokenFromControl(control string) string {
	token, _, _ := strings.Cut(control, ":")
	return token
}

func displayInvitationLink(t *testing.T, display string) string {
	t.Helper()
	_, section, ok := strings.Cut(display, "\n\nBrowser invitation link:\n")
	line, _, _ := strings.Cut(section, "\n")
	link := line[strings.LastIndexByte(line, ' ')+1:]
	if !ok || !strings.Contains(link, "/c/") {
		t.Fatal("connection instructions are missing the browser invitation link")
	}
	return link
}

func displayShortCode(t *testing.T, display string) string {
	t.Helper()
	code := path.Base(displayInvitationLink(t, display))
	if len(code) != shortCodeLength {
		t.Fatal("connection instructions are missing the short-code link")
	}
	return code
}

func redeemBrowserInvitation(ctx context.Context, link string) (string, error) {
	if !strings.Contains(link, "://") {
		link = "http://" + link
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, link, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(version.Header, version.Protocol)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("redeem browser invitation: HTTP %d", resp.StatusCode)
	}
	var result struct {
		Token string `json:"token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	return result.Token, err
}
