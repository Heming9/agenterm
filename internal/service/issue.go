package service

import (
	"crypto/sha256"
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
	controlKey := randomBytes(32)
	controlHash := sha256.Sum256([]byte(controlKey))
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
	// The public identifier is independently random and unique among live tokens.
	var identifier, token string
	for {
		identifier = randomManagementKey()[:6]
		duplicate := false
		for existing := range s.tokens {
			decoded, err := protocol.Decode(existing)
			if err == nil && decoded.Identifier == identifier {
				duplicate = true
				break
			}
		}
		if !duplicate {
			break
		}
	}
	token, err := protocol.Encode(protocol.Token{URL: s.clientURL, Secret: secret, Identifier: identifier})
	if err != nil {
		return nil, err
	}
	if s.multiDevice == nil {
		s.multiDevice = make(map[string]bool)
	}
	value, err := s.newInvitation(token, identifier, expires)
	if err != nil {
		return nil, err
	}
	valueExpires := expires
	var code string
	var codeExpires time.Time
	if !multiDevice {
		if s.shortCodes == nil {
			s.shortCodes = make(shortCodeStore)
		}
		code, err = s.shortCodes.issue(token, time.Now())
		if err != nil {
			decoded, _ := protocol.Decode(value)
			delete(s.invitations, decoded.Secret)
			return nil, err
		}
		codeExpires = s.shortCodes[code].expires
		if codeExpires.After(expires) {
			expires = codeExpires
		}
	}
	s.multiDevice[secret] = multiDevice
	s.tokens[token] = secret
	// Track lifecycle even when IP quotas are disabled.
	if s.clientOwners == nil {
		s.clientOwners = make(map[string]*clientOwner)
	}
	s.nextClientOrder++
	s.clientOwners[secret] = &clientOwner{ip: owner, token: token, controlKeyHash: controlHash, order: s.nextClientOrder, expires: expires}
	result := map[string]any{"client_token": token, "control_token": token + ":" + controlKey, "token_identifier": identifier, "multi_device": multiDevice, "invitation": value, "expires_at": valueExpires.UTC().Format(time.RFC3339), "token_initial_expires_at": expires.UTC().Format(time.RFC3339)}
	if code != "" {
		result["short_code"] = code
		result["short_code_expires_at"] = codeExpires.UTC().Format(time.RFC3339)
	}
	shell, powershell := installCommands(token)
	result["macos_linux_command"] = shell
	result["windows_powershell_command"] = powershell
	result["display_text"] = connectionDisplayText(token, shell, powershell, code, timeoutSeconds, valueExpires, codeExpires, expires, multiDevice)
	return result, nil
}

// Presentation belongs to the replaceable frontend too: a retained runtime may
// still return an older display_text while its credentials and commands remain
// valid. Never reconstruct or change those credentials when updating the prose.
func connectionDisplayText(token, shell, powershell, code string, timeoutSeconds int64, expires, codeExpires, tokenExpires time.Time, multiDevice bool) string {
	shortSection, shortUsage := "", ""
	if code != "" {
		shortSection = "\n\nShort Code:\n" + code
		shortUsage = i18n.Text("en", "connection.short.code.usage")
	}
	text := fmt.Sprintf(i18n.Text("en", "connection.display.text"), powershell, shell, token, shortSection, shortUsage, timeoutSeconds, expires.UTC().Format("2006-01-02 15:04:05"))
	if code != "" {
		text += fmt.Sprintf("\nShort Code validity: 3 minutes, single use (expires at %s UTC).", codeExpires.UTC().Format("2006-01-02 15:04:05"))
	}
	text += fmt.Sprintf("\nBefore the first connection, the connection token and one-click commands expire at %s UTC.", tokenExpires.UTC().Format("2006-01-02 15:04:05"))
	if multiDevice {
		text += "\n\n" + i18n.Text("en", "multi.device.display.notice")
	}
	return text
}
