package main

import (
	"fmt"
	"time"

	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
)

// createClient issues a single-device credential and a single-use invitation.
func (s *server) createClient(owner string) (map[string]any, error) {
	return s.createClientMode(owner, false)
}

func (s *server) createClientMode(owner string, multiDevice bool) (map[string]any, error) {
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
	if s.tokens == nil {
		s.tokens = make(map[string]string)
	}
	if s.invitations == nil {
		s.invitations = make(map[string]invitation)
	}
	for key, entry := range s.invitations {
		if time.Now().After(entry.expires) {
			delete(s.invitations, key)
			if secret := s.tokens[entry.token]; s.clientOwners[secret] != nil && !s.clientOwners[secret].used {
				delete(s.clientOwners, secret)
				delete(s.tokens, entry.token)
				delete(s.multiDevice, secret)
			}
		}
	}
	if s.maxClientsPerIP > 0 {
		if owner == "" {
			return nil, i18n.NewError("client.ip.invalid")
		}
		online := s.onlineForIP(owner)
		if online >= s.maxClientsPerIP {
			return nil, i18n.NewError("client.ip.limit.reached")
		}
		s.trimPending(owner, s.maxClientsPerIP-online-1)
	}
	if s.multiDevice == nil {
		s.multiDevice = make(map[string]bool)
	}
	s.multiDevice[secret] = multiDevice
	s.tokens[token] = secret
	s.invitations[inviteSecret] = invitation{token: token, expires: expires}
	if s.maxClientsPerIP > 0 {
		if s.clientOwners == nil {
			s.clientOwners = make(map[string]*clientOwner)
		}
		s.nextClientOrder++
		s.clientOwners[secret] = &clientOwner{ip: owner, token: token, order: s.nextClientOrder}
	}
	result := map[string]any{"client_token": token, "multi_device": multiDevice, "invitation": value, "expires_at": expires.UTC().Format(time.RFC3339)}
	unavailable := i18n.Text(i18n.Terminal(), "unavailable.use.a.release.build.of.the")
	shell, powershell := unavailable, unavailable
	if validReleaseTag.MatchString(releaseTag) {
		shell, powershell = installCommands(value)
		result["macos_linux_command"] = shell
		result["windows_powershell_command"] = powershell
	}
	result["display_text"] = fmt.Sprintf(i18n.Text(i18n.Terminal(), "connection.token.s.without.a.client.macos"), token, shell, powershell)
	if multiDevice {
		result["display_text"] = serverText("multi.device.display.notice") + "\n\n" + result["display_text"].(string)
	}
	return result, nil
}
