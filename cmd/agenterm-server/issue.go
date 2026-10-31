package main

import (
	"fmt"
	"time"

	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
)

// createClient issues a new isolated client credential and a single-use invitation.
func (s *server) createClient() (map[string]any, error) {
	secret := randomBytes(protocol.TokenSecretSize)
	token, err := protocol.Encode(protocol.Token{URL: s.clientURL, Secret: secret})
	if err != nil {
		return nil, err
	}
	inviteSecret := randomBytes(protocol.TokenSecretSize)
	value, err := protocol.Encode(protocol.Token{URL: s.clientURL, Secret: inviteSecret})
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(15 * time.Minute)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tokens) >= 1000 {
		return nil, fmt.Errorf("client limit reached")
	}
	if s.tokens == nil {
		s.tokens = make(map[string]string)
	}
	if s.invitations == nil {
		s.invitations = make(map[string]invitation)
	}
	for key, entry := range s.invitations {
		if time.Now().After(entry.expires) {
			delete(s.invitations, key)
		}
	}
	s.tokens[token] = secret
	s.invitations[inviteSecret] = invitation{token: token, expires: expires}
	result := map[string]any{"client_token": token, "invitation": value, "expires_at": expires.UTC().Format(time.RFC3339)}
	unavailable := i18n.Text(i18n.Terminal(), "Unavailable: use a release build of the server", "不可用：请使用发布版服务端")
	shell, powershell := unavailable, unavailable
	if validReleaseTag.MatchString(releaseTag) {
		shell, powershell = installCommands(value)
		result["macos_linux_command"] = shell
		result["windows_powershell_command"] = powershell
	}
	result["display_text"] = fmt.Sprintf(i18n.Text(i18n.Terminal(), "Connection token:\n%s\n\nWithout a client\nmacOS / Linux connection command:\n%s\n\nWindows connection command:\n%s", "连接 token：\n%s\n\n无客户端\nmacOS / Linux 一键连接命令：\n%s\n\nWindows 一键连接命令：\n%s"), token, shell, powershell)
	return result, nil
}
