package service

import (
	"crypto/rand"
	"math/big"
	"time"
)

const shortCodeTTL = 3 * time.Minute

const shortCodeAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

type shortCodeEntry struct {
	token   string
	expires time.Time
}

// shortCodeStore is guarded by runtimeState.mu. Codes are independent random
// credentials, and never derived from the connection token or invitation.
type shortCodeStore map[string]shortCodeEntry

func (s shortCodeStore) issue(token string, now time.Time) (string, error) {
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
		if _, exists := s[value]; !exists {
			s[value] = shortCodeEntry{token: token, expires: now.Add(shortCodeTTL)}
			return value, nil
		}
	}
}

func (s shortCodeStore) lookup(code string, now time.Time) (string, bool) {
	entry, ok := s[code]
	if !ok {
		return "", false
	}
	if !now.Before(entry.expires) {
		delete(s, code)
		return "", false
	}
	return entry.token, true
}
