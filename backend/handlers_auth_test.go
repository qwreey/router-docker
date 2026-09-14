package main

import (
	"net/http/httptest"
	"testing"
)

// TestRateLimitKeyOnUnixListener is the 2026-09-07 security review's finding
// H2: router-manager normally listens on a unix socket, so every caller
// shares one r.RemoteAddr and therefore shared one authgate lockout bucket -
// five wrong guesses from anything that could reach /router/ locked the
// operator out of their own router. X-Real-IP is what tells callers apart,
// and router's own nginx is the only thing that can dial that socket and
// always overwrites the header.
func TestRateLimitKeyOnUnixListener(t *testing.T) {
	listenerIsUnix = true
	t.Cleanup(func() { listenerIsUnix = false })

	req := httptest.NewRequest("POST", "/api/auth/unlock", nil)
	req.RemoteAddr = "@" // what a unix-socket peer actually looks like
	req.Header.Set("X-Real-IP", "203.0.113.7")
	if got := rateLimitKey(req); got != "203.0.113.7" {
		t.Fatalf("rateLimitKey(unix + X-Real-IP) = %q, want the header value", got)
	}

	other := httptest.NewRequest("POST", "/api/auth/unlock", nil)
	other.RemoteAddr = "@"
	other.Header.Set("X-Real-IP", "203.0.113.8")
	if rateLimitKey(req) == rateLimitKey(other) {
		t.Fatalf("two clients still share one lockout bucket")
	}

	// Nothing but router's own nginx should ever reach the socket, but if
	// something does, fall back to the peer key rather than to an absent
	// (empty) key that every such caller would then share as "".
	bare := httptest.NewRequest("POST", "/api/auth/unlock", nil)
	bare.RemoteAddr = "@"
	if got := rateLimitKey(bare); got != "@" {
		t.Fatalf("rateLimitKey(unix, no header) = %q, want the peer key %q", got, "@")
	}
}

// TestRateLimitKeyOnTCPListenerIgnoresHeader pins the other half: with
// ROUTER_MANAGER_ADDR set (the documented local-dev escape hatch) any direct
// caller can set X-Real-IP, so trusting it would let an attacker reset their
// own lockout on every request - strictly worse than one shared bucket.
func TestRateLimitKeyOnTCPListenerIgnoresHeader(t *testing.T) {
	listenerIsUnix = false

	req := httptest.NewRequest("POST", "/api/auth/unlock", nil)
	req.RemoteAddr = "198.51.100.4:51234"
	req.Header.Set("X-Real-IP", "203.0.113.7")
	if got := rateLimitKey(req); got != "198.51.100.4" {
		t.Fatalf("rateLimitKey(tcp) = %q, want the real peer address, not the forgeable header", got)
	}
}
