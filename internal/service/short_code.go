package service

import (
	"crypto/rand"
	"math/big"
	"strings"
)

const shortCodeAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

const maxShortCodeRedemptions = 10

type shortCodeEntry struct {
	token       string
	redemptions int
}

// shortCodeStore is guarded by runtimeState.mu. Codes are independent random
// credentials, and never derived from the connection token or invitation.
type shortCodeStore map[string]shortCodeEntry

func (s shortCodeStore) issue(token string) (string, error) {
	for {
		var code [shortCodeLength]byte
		for i := range code {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(shortCodeAlphabet))))
			if err != nil {
				return "", err
			}
			code[i] = shortCodeAlphabet[n.Int64()]
		}
		value := string(code[:])
		if s.available(value) {
			s[value] = shortCodeEntry{token: token}
			return value, nil
		}
	}
}

// Avoid collisions with legacy uppercase codes.
func (s shortCodeStore) available(code string) bool {
	_, exists := s[code]
	_, legacyExists := s[strings.ToUpper(code)]
	return !exists && !legacyExists
}

func (s shortCodeStore) lookup(code string) (string, bool) {
	entry, ok := s[code]
	if !ok {
		entry, ok = s[strings.ToLower(code)]
	}
	return entry.token, ok
}
