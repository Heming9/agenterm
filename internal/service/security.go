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
	failures                  int
	lastFailure, blockedUntil time.Time
}
type authLimiter struct {
	mu      sync.Mutex
	entries map[string]failureEntry
}

const failureLimit = 3
const maxFailureBlock = 10 * time.Minute
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

// checkWithReset atomically applies the limit; non-consuming lookups retain failure history.
func (l *authLimiter) checkWithReset(ip string, valid, resetOnSuccess bool, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string]failureEntry)
	}
	e := l.entries[ip]
	if now.Before(e.blockedUntil) {
		return e.blockedUntil.Sub(now)
	}
	if valid {
		if resetOnSuccess {
			delete(l.entries, ip)
		}
		return 0
	}
	if _, exists := l.entries[ip]; !exists && len(l.entries) >= maxFailureEntries {
		// Retain actively blocked entries; evict the least recently active
		// unblocked source. If all are blocked, retry at the earliest expiry.
		var oldestIP string
		var oldest, earliestBlocked time.Time
		for key, item := range l.entries {
			if now.Before(item.blockedUntil) {
				if earliestBlocked.IsZero() || item.blockedUntil.Before(earliestBlocked) {
					earliestBlocked = item.blockedUntil
				}
			} else if oldestIP == "" || item.lastFailure.Before(oldest) {
				oldestIP, oldest = key, item.lastFailure
			}
		}
		if oldestIP == "" {
			return earliestBlocked.Sub(now)
		}
		delete(l.entries, oldestIP)
	}
	// Saturation avoids integer overflow after prolonged abuse.
	if e.failures < failureLimit-1+int(maxFailureBlock/(2*time.Second)) {
		e.failures++
	}
	e.lastFailure = now
	if e.failures >= failureLimit {
		delay := time.Duration(e.failures-failureLimit+1) * 2 * time.Second
		e.blockedUntil = now.Add(delay)
	}
	l.entries[ip] = e
	return 0
}

type authWaitError struct{ wait time.Duration }

func (e *authWaitError) Error() string {
	return i18n.Text(i18n.Terminal(), "too.many.authentication.failures.try.again.later")
}
func retrySeconds(wait time.Duration) int { return int((wait + time.Second - 1) / time.Second) }

// Long credentials remain authenticated but do not use the short-code limiter.
func (s *runtimeState) authorize(r *http.Request, valid bool) (int, error) {
	if _, err := s.requestIP(r); err != nil {
		return http.StatusBadRequest, err
	}
	if !valid {
		return http.StatusUnauthorized, i18n.NewError("unauthorized")
	}
	return 0, nil
}

func (s *runtimeState) authorizeShortCode(r *http.Request, valid, consume bool) (int, error) {
	return s.authorizeLimited(r, valid, consume, &s.limiter)
}

func (s *runtimeState) authorizeAdmin(r *http.Request, valid bool) (int, error) {
	return s.authorizeLimited(r, valid, true, &s.adminLimiter)
}

func (s *runtimeState) authorizeLimited(r *http.Request, valid, resetOnSuccess bool, limiter *authLimiter) (int, error) {
	ip, err := s.requestIP(r)
	if err != nil {
		return http.StatusBadRequest, err
	}
	if wait := limiter.checkWithReset(ip, valid, resetOnSuccess, time.Now()); wait > 0 {
		return http.StatusTooManyRequests, &authWaitError{wait: wait}
	}
	if !valid {
		return http.StatusUnauthorized, i18n.NewError("unauthorized")
	}
	return 0, nil
}
