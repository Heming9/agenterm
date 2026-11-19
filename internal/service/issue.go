package service

import (
	"crypto/sha256"
	"fmt"
	"time"

	"agenterm/internal/i18n"
	"agenterm/internal/protocol"
)

const defaultInvitationTimeoutSeconds int64 = 900
const maxInvitationTimeoutSeconds int64 = (1<<63 - 1) / int64(time.Second)

func (s *runtimeState) createClientMode(owner string, args createClientArgs) (map[string]any, error) {
	multiDevice, timeoutSeconds := args.MultiDevice, args.TimeoutSeconds
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
		online, occupied := s.slotsForIP(owner)
		if occupied >= s.maxClientsPerIP {
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
	var code string
	if !multiDevice && s.enableShortCode {
		if s.shortCodes == nil {
			s.shortCodes = make(shortCodeStore)
		}
		code, err = s.shortCodes.issue(token)
		if err != nil {
			decoded, _ := protocol.Decode(value)
			delete(s.invitations, decoded.Secret)
			return nil, err
		}
	}
	s.multiDevice[secret] = multiDevice
	s.tokens[token] = secret
	// Track lifecycle even when IP quotas are disabled.
	if s.clientOwners == nil {
		s.clientOwners = make(map[string]*clientOwner)
	}
	s.nextClientOrder++
	s.clientOwners[secret] = &clientOwner{ip: owner, token: token, controlKeyHash: controlHash, order: s.nextClientOrder, expires: expires, temporaryAssistance: args.TemporaryAssistance}
	deadline := expires.UTC().Format(time.RFC3339)
	result := map[string]any{"client_token": token, "control_token": token + ":" + controlKey, "token_identifier": identifier, "multi_device": multiDevice, "invitation": value, "expires_at": deadline, "token_initial_expires_at": deadline}
	if args.TemporaryAssistance {
		result["temporary_assistance"] = true
	}
	if code != "" {
		result["short_code"] = code
		result["short_code_expires_at"] = deadline
	}
	setConnectionPresentation(result, token, code, args, expires)
	return result, nil
}

// The replaceable frontend also uses this presentation when a retained
// runtime supplies credentials in its older display format.
func setConnectionPresentation(result map[string]any, token, code string, args createClientArgs, expires time.Time) string {
	result["invitation_url"] = browserInvitationURL(token, "")
	if code != "" {
		result["short_code_url"] = browserInvitationURL(token, code)
	}
	shell, powershell := installCommands(token, code)
	result["macos_linux_command"] = shell
	result["windows_powershell_command"] = powershell
	text := connectionDisplayText(token, shell, powershell, code, args, expires)
	result["display_text"] = text
	return text
}

func connectionDisplayText(token, shell, powershell, code string, args createClientArgs, expires time.Time) string {
	expiry := expires.UTC().Format("2006-01-02 15:04:05")
	link := browserInvitationURL(token, code)
	var text string
	if args.TemporaryAssistance {
		browser := "\n\nBrowser invitation link:\n" + link
		text = fmt.Sprintf(i18n.Text("en", "connection.display.temporary.assistance"), powershell, shell, browser, args.TimeoutSeconds, expiry)
	} else {
		label := "Connection token:"
		tokenAlternative := ""
		if code != "" {
			label = "Connection token / short code ( " + code + " ):"
			tokenAlternative = " or short code above"
		}
		connectionSection := label + "\n```\n" + token + "\n```\n\nBrowser invitation link:\nAgenterm invites you to a remote assistance session (open the invitation link in a computer browser): " + link
		text = fmt.Sprintf(i18n.Text("en", "connection.display.text"), powershell, shell, connectionSection, "open the browser invitation link above or run the command for your system.", tokenAlternative, args.TimeoutSeconds, expiry)
	}
	if args.MultiDevice {
		text += "\n\n" + i18n.Text("en", "multi.device.display.notice")
	}
	return text
}
