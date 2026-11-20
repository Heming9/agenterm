package service

import (
	"crypto/sha256"
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
	temporaryOnly := args.ConnectionMode == protocol.TemporaryConnectionMode
	if args.ConnectionMode != "" && !temporaryOnly {
		return nil, i18n.NewError("mcp.connection.mode.invalid")
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
	token, err := protocol.Encode(protocol.Token{URL: s.clientURL, Secret: secret, Identifier: identifier, TemporaryOnly: temporaryOnly})
	if err != nil {
		return nil, err
	}
	if s.multiDevice == nil {
		s.multiDevice = make(map[string]bool)
	}
	var code string
	if !multiDevice && s.enableShortCode {
		if s.shortCodes == nil {
			s.shortCodes = make(shortCodeStore)
		}
		code, err = s.shortCodes.issue(token)
		if err != nil {
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
	s.clientOwners[secret] = &clientOwner{ip: owner, token: token, controlKeyHash: controlHash, order: s.nextClientOrder, expires: expires, temporaryOnly: temporaryOnly}
	deadline := expires.UTC().Format(time.RFC3339)
	result := map[string]any{"client_token": token, "control_token": token + ":" + controlKey, "token_identifier": identifier, "multi_device": multiDevice, "expires_at": deadline, "token_initial_expires_at": deadline}
	if code != "" {
		result["short_code"] = code
		result["short_code_expires_at"] = deadline
	}
	return result, nil
}
