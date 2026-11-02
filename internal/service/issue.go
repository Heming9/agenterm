package service

import (
	"fmt"
	"time"

	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
)

const defaultInvitationTimeoutSeconds int64 = 300
const maxInvitationTimeoutSeconds int64 = (1<<63 - 1) / int64(time.Second)

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
	shell, powershell := installCommands(value)
	result["macos_linux_command"] = shell
	result["windows_powershell_command"] = powershell
	result["display_text"] = connectionDisplayText(token, shell, powershell, timeoutSeconds, expires, multiDevice)
	return result, nil
}

// Presentation belongs to the replaceable frontend too: a retained runtime may
// still return an older display_text while its credentials and commands remain
// valid. Never reconstruct or change those credentials when updating the prose.
func connectionDisplayText(token, shell, powershell string, timeoutSeconds int64, expires time.Time, multiDevice bool) string {
	text := fmt.Sprintf(i18n.Text("en", "connection.display.text"), powershell, shell, token, timeoutSeconds, expires.UTC().Format("2006-01-02 15:04:05"))
	if multiDevice {
		text += "\n\n" + i18n.Text("en", "multi.device.display.notice")
	}
	return text
}
