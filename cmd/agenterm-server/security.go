package main

import (
	"fmt"
	"net"
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

// check combines the limit check and failure recording so concurrent attempts cannot bypass it.
func (l *authLimiter) check(remote string, valid bool, now time.Time) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string]failureEntry)
	}
	e := l.entries[host]
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
	if _, exists := l.entries[host]; !exists && len(l.entries) >= maxFailureEntries {
		for ip, item := range l.entries {
			if now.Sub(item.start) >= failureWindow && !now.Before(item.blockedUntil) {
				delete(l.entries, ip)
			}
		}
		if len(l.entries) >= maxFailureEntries {
			return false
		}
	}
	l.entries[host] = e
	return true
}

func (s *server) authorize(remote string, valid bool) (int, error) {
	if !s.limiter.check(remote, valid, time.Now()) {
		return 429, fmt.Errorf("too many authentication failures; try again later")
	}
	if !valid {
		return 401, fmt.Errorf("unauthorized")
	}
	return 0, nil
}
