package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A single passphrase guarding a publicly reachable page is a guessing game at internet speed: five
// attempts per IP per window turns that into a non-event, and the delay already in the login path removes
// the timing signal. In-memory on purpose: Iris is one process, and a restart clearing the counters is a
// smaller problem than a database write on every bad guess.
const (
	maxLoginFailures = 5
	loginWindow      = 15 * time.Minute
)

type attemptLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	now      func() time.Time
}

func newAttemptLimiter() *attemptLimiter {
	return &attemptLimiter{failures: make(map[string][]time.Time), now: time.Now}
}

// blocked reports whether the key has used up its attempts, and how long until it may try again.
func (l *attemptLimiter) blocked(key string) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.prune(key, now)
	if len(kept) < maxLoginFailures {
		return false, 0
	}
	return true, loginWindow - now.Sub(kept[0])
}

func (l *attemptLimiter) fail(key string) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[key] = append(l.prune(key, now), now)
}

func (l *attemptLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

// prune drops attempts older than the window and returns what is still current.
func (l *attemptLimiter) prune(key string, now time.Time) []time.Time {
	kept := l.failures[key][:0]
	for _, at := range l.failures[key] {
		if now.Sub(at) < loginWindow {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, key)
		return nil
	}
	l.failures[key] = kept
	return kept
}

// clientKey identifies the caller for the limiter.
//
// Behind cloudflared every request arrives from 127.0.0.1, so RemoteAddr would lump the whole internet into
// one bucket. CF-Connecting-IP is only trusted when the connection itself is loopback, because that header
// can be forged by anyone who can reach Iris directly.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && (host == "127.0.0.1" || host == "::1") {
		if forwarded := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); forwarded != "" {
			return "cf:" + forwarded
		}
	}
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
