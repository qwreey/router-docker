package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"router/internal/authgate"
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

// authRequest drives one auth handler against the package-level gate.
func authRequest(t *testing.T, h http.HandlerFunc, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/auth/x", strings.NewReader(body))
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func unlockCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == authgate.CookieName {
			return c
		}
	}
	t.Fatalf("no unlock cookie in response (status %d): %s", rec.Code, rec.Body)
	return nil
}

// TestStatusWeakPasswordOnlyForUnlockedRequests: telling an unauthenticated
// client that the password is short would hand an attacker a hint, so the
// flag may only ride on a response to a request carrying a valid session.
func TestStatusWeakPasswordOnlyForUnlockedRequests(t *testing.T) {
	old := gate
	t.Cleanup(func() { gate = old })

	hash, err := authgate.HashPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	gate = authgate.New(hash, "")

	// Before anyone unlocks, nobody learns anything.
	if rec := authRequest(t, handleAuthStatus, ""); strings.Contains(rec.Body.String(), "weakPassword") {
		t.Fatalf("status leaked weakPassword before any unlock: %s", rec.Body)
	}

	rec := authRequest(t, handleAuthUnlock, `{"password":"hunter2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unlock status = %d: %s", rec.Code, rec.Body)
	}
	var unlock map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &unlock); err != nil || !unlock["weakPassword"] {
		t.Fatalf("unlock response = %s, want weakPassword true", rec.Body)
	}
	cookie := unlockCookie(t, rec)

	// The gate now knows the password is short, yet an unauthenticated and a
	// forged-cookie request must still not see it.
	for name, cookies := range map[string][]*http.Cookie{
		"no cookie":     nil,
		"forged cookie": {{Name: authgate.CookieName, Value: "AAAA.BBBB"}},
	} {
		if body := authRequest(t, handleAuthStatus, "", cookies...).Body.String(); strings.Contains(body, "weakPassword") {
			t.Fatalf("status leaked weakPassword with %s: %s", name, body)
		}
	}

	var status authStatusResponse
	rec = authRequest(t, handleAuthStatus, "", cookie)
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Unlocked || !status.WeakPassword {
		t.Fatalf("status for an unlocked request = %s, want unlocked and weakPassword", rec.Body)
	}
}

func TestSetupAndChangeHandlersAnswer400ForShortPasswords(t *testing.T) {
	old := gate
	t.Cleanup(func() { gate = old })
	gate = authgate.New("", filepath.Join(t.TempDir(), "auth-hash.json"))
	var token string
	gate.AnnounceSetupToken(func(tok string) { token = tok })

	rec := authRequest(t, handleAuthSetup, `{"token":"`+token+`","password":"elevenchars"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "at least 12 characters") {
		t.Fatalf("short setup = %d %s, want 400 naming the minimum", rec.Code, rec.Body)
	}
	good := "twelve-chars"
	if rec := authRequest(t, handleAuthSetup, `{"token":"`+token+`","password":"`+good+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("12-char setup = %d %s", rec.Code, rec.Body)
	}

	rec = authRequest(t, handleAuthChange, `{"currentPassword":"`+good+`","newPassword":"elevenchars"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "at least 12 characters") {
		t.Fatalf("short change = %d %s, want 400 naming the minimum", rec.Code, rec.Body)
	}
}
