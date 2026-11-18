package auth

import (
	"net/http"
	"testing"
	"time"
)

func TestLimiterBlocksAfterFiveFailuresAndRecovers(t *testing.T) {
	l := newAttemptLimiter()
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	for i := 0; i < maxLoginFailures; i++ {
		if blocked, _ := l.blocked("cf:1.2.3.4"); blocked {
			t.Fatalf("blocked after only %d failures", i)
		}
		l.fail("cf:1.2.3.4")
	}
	blocked, wait := l.blocked("cf:1.2.3.4")
	if !blocked {
		t.Fatal("five failures must block the key")
	}
	if wait <= 0 || wait > loginWindow {
		t.Fatalf("wait = %s, want within the window", wait)
	}
	// another key is unaffected: one bad actor must not lock Boss out
	if blocked, _ := l.blocked("cf:9.9.9.9"); blocked {
		t.Fatal("a different client was blocked")
	}
	// and the window expires
	now = now.Add(loginWindow + time.Minute)
	if blocked, _ := l.blocked("cf:1.2.3.4"); blocked {
		t.Fatal("still blocked after the window")
	}
}

func TestLimiterForgivesAfterSuccess(t *testing.T) {
	l := newAttemptLimiter()
	for i := 0; i < maxLoginFailures-1; i++ {
		l.fail("cf:1.2.3.4")
	}
	l.reset("cf:1.2.3.4")
	for i := 0; i < maxLoginFailures; i++ {
		if blocked, _ := l.blocked("cf:1.2.3.4"); blocked {
			t.Fatalf("blocked after a successful sign-in at attempt %d", i)
		}
		l.fail("cf:1.2.3.4")
	}
}

func TestClientKeyTrustsTheHeaderOnlyFromLoopback(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "/api/auth/login", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("CF-Connecting-IP", "203.0.113.7")
	if got := clientKey(r); got != "cf:203.0.113.7" {
		t.Fatalf("loopback + header = %q", got)
	}

	// A direct internet caller could forge the header; the socket address must win.
	direct, _ := http.NewRequest(http.MethodPost, "/", nil)
	direct.RemoteAddr = "198.51.100.9:443"
	direct.Header.Set("CF-Connecting-IP", "203.0.113.7")
	if got := clientKey(direct); got != "198.51.100.9" {
		t.Fatalf("non-loopback key = %q, want the socket address", got)
	}
}
