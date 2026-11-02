package service

import (
	"agenterm/internal/i18n"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type failureEntry struct {
	failures            int
	start, blockedUntil time.Time
}
type authLimiter struct {
	mu      sync.Mutex
	entries map[string]failureEntry
}

const failureLimit = 10
const failureWindow = time.Minute
const failureBlock = 5 * time.Minute
const maxFailureEntries = 10000

// Authentication and issuance quotas share one IP source. Headers are trusted
// only when explicitly configured; that proxy must overwrite the chosen header.
func (s *runtimeState) requestIP(r *http.Request) (string, error) {
	var raw string
	if s.clientIPHeader == "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return "", i18n.NewError("client.ip.invalid")
		}
		raw = host
	} else {
		values := r.Header.Values(s.clientIPHeader)
		if len(values) != 1 {
			return "", i18n.NewError("client.ip.invalid")
		}
		raw = strings.TrimSpace(values[0])
	}
	ip, err := netip.ParseAddr(raw)
	if err != nil || ip.Zone() != "" {
		return "", i18n.NewError("client.ip.invalid")
	}
	return ip.Unmap().String(), nil
}

// check combines the limit check and failure recording so concurrent attempts cannot bypass it.
func (l *authLimiter) check(ip string, valid bool, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string]failureEntry)
	}
	e := l.entries[ip]
	if now.Before(e.blockedUntil) {
		return false
	}
	if valid {
		return true
	}
	if e.start.IsZero() || now.Sub(e.start) >= failureWindow || !e.blockedUntil.IsZero() {
		e = failureEntry{start: now}
	}
	e.failures++
	if e.failures >= failureLimit {
		e.blockedUntil = now.Add(failureBlock)
	}
	if _, exists := l.entries[ip]; !exists && len(l.entries) >= maxFailureEntries {
		for ip, item := range l.entries {
			if now.Sub(item.start) >= failureWindow && !now.Before(item.blockedUntil) {
				delete(l.entries, ip)
			}
		}
		if len(l.entries) >= maxFailureEntries {
			return false
		}
	}
	l.entries[ip] = e
	return true
}

func (s *runtimeState) authorize(r *http.Request, valid bool) (int, error) {
	ip, err := s.requestIP(r)
	if err != nil {
		return http.StatusBadRequest, err
	}
	if !s.limiter.check(ip, valid, time.Now()) {
		return 429, i18n.NewError("too.many.authentication.failures.try.again.later")
	}
	if !valid {
		return 401, i18n.NewError("unauthorized")
	}
	return 0, nil
}
