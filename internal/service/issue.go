package service

import (
	"fmt"
	"time"

	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
)

const defaultInvitationTimeoutSeconds int64 = 300
const maxInvitationTimeoutSeconds int64 = (1<<63 - 1) / int64(time.Second)

// createClient issues a single-device credential and a single-use invitation.
func (s *runtimeState) createClient(owner string) (map[string]any, error) {
	return s.createClientMode(owner, false, defaultInvitationTimeoutSeconds)
}

func (s *runtimeState) createClientMode(owner string, multiDevice bool, timeoutSeconds int64) (map[string]any, error) {
	if timeoutSeconds < 1 || timeoutSeconds > maxInvitationTimeoutSeconds {
		return nil, i18n.NewError("invitation.timeout.invalid", maxInvitationTimeoutSeconds)
	}
	secret := randomBytes(protocol.TokenSecretSize)
	token, err := protocol.Encode(protocol.Token{URL: s.clientURL, Secret: secret})
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens == nil {
		s.tokens = make(map[string]string)
	}
	s.cleanupTokensLocked(time.Now())
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
	value, err := s.newInvitation(token, expires)
	if err != nil {
		return nil, err
	}
	s.multiDevice[secret] = multiDevice
	s.tokens[token] = secret
	// Track lifecycle even when IP quotas are disabled.
	if s.clientOwners == nil {
		s.clientOwners = make(map[string]*clientOwner)
	}
	s.nextClientOrder++
	s.clientOwners[secret] = &clientOwner{ip: owner, token: token, order: s.nextClientOrder, expires: expires}
	result := map[string]any{"client_token": token, "multi_device": multiDevice, "invitation": value, "expires_at": expires.UTC().Format(time.RFC3339)}
	result["next_action"] = map[string]any{
		"tool": "list_clients", "arguments": map[string]any{"client_token": token, "wait_ms": 20000},
		"until": result["expires_at"], "instructions": serverText("mcp.connection.workflow"),
	}
	shell, powershell := installCommands(value)
	result["macos_linux_command"] = shell
	result["windows_powershell_command"] = powershell
	result["display_text"] = fmt.Sprintf(i18n.Text(i18n.Terminal(), "connection.token.s.without.a.client.macos"), token, shell, powershell)
	if multiDevice {
		result["display_text"] = fmt.Sprintf(serverText("multi.device.display.notice"), timeoutSeconds) + "\n\n" + result["display_text"].(string)
	}
	return result, nil
}
