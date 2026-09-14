package authgate

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler records whether the gated handler was reached at all - the whole
// point of these tests is what does and does not get through.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

// TestRequirePasswordFailsClosedWhenUnconfigured is the regression test for
// the 2026-09-07 security review's finding C2: this middleware used to call
// next.ServeHTTP unconditionally when no password was configured, which is
// the default state of a fresh install - so every route it wrapped (netgate
// egress rules, DNS resolver, inbound forwards, tinyauth users) was an
// unauthenticated admin API.
func TestRequirePasswordFailsClosedWhenUnconfigured(t *testing.T) {
	g := New("", "") // no env pin, no store: Configured() == false
	if g.Configured() {
		t.Fatalf("New(\"\", \"\").Configured() = true, want false")
	}

	reached := false
	rec := httptest.NewRecorder()
	g.RequirePassword(okHandler(&reached)).ServeHTTP(rec, httptest.NewRequest("PUT", "/api/netgate/outbound", nil))

	if reached {
		t.Fatalf("gated handler ran with no password configured - RequirePassword is failing open")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (503, not 401: there is no password to prompt for yet)", rec.Code, http.StatusServiceUnavailable)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if body := rec.Body.String(); body == "" || body[0] != '{' {
		t.Fatalf("body = %q, want a JSON error object the frontend can surface", body)
	}
}

// TestRequirePasswordUnauthorizedWhenConfiguredButLocked pins the other
// branch: once a password exists, a missing/invalid cookie must still be a
// 401 and not the 503 above, because 401 is what api/client.ts keys the
// unlock prompt off.
func TestRequirePasswordUnauthorizedWhenConfiguredButLocked(t *testing.T) {
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	g := New(hash, "")

	reached := false
	rec := httptest.NewRecorder()
	g.RequirePassword(okHandler(&reached)).ServeHTTP(rec, httptest.NewRequest("PUT", "/api/netgate/outbound", nil))

	if reached {
		t.Fatalf("gated handler ran without an unlock cookie")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestRequirePasswordAllowsUnlockedRequest is the "and it still works"
// half - a valid cookie from TryUnlock passes through.
func TestRequirePasswordAllowsUnlockedRequest(t *testing.T) {
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	g := New(hash, "")

	token, ok, err := g.TryUnlock("10.0.0.1", "hunter2")
	if err != nil || !ok {
		t.Fatalf("TryUnlock(correct password) = (ok=%v, err=%v), want (true, nil)", ok, err)
	}

	req := httptest.NewRequest("PUT", "/api/netgate/outbound", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})

	reached := false
	rec := httptest.NewRecorder()
	g.RequirePassword(okHandler(&reached)).ServeHTTP(rec, req)

	if !reached {
		t.Fatalf("gated handler did not run for an unlocked request (status %d)", rec.Code)
	}
}

// TestRateLimitBucketsAreIndependent guards the invariant H2's fix depends
// on: distinct keys must not share a lockout. The fix itself is in
// handlers_auth.go's rateLimitKey (which key gets passed here); this pins
// that the Gate really does count them separately, so that change has an
// effect at all.
func TestRateLimitBucketsAreIndependent(t *testing.T) {
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	g := New(hash, "")

	// maxFailuresBeforeLockout wrong guesses from one client.
	for i := 0; i < maxFailuresBeforeLockout; i++ {
		if _, ok, _ := g.TryUnlock("10.0.0.1", "wrong"); ok {
			t.Fatalf("TryUnlock(wrong password) succeeded")
		}
	}
	if _, _, err := g.TryUnlock("10.0.0.1", "hunter2"); err == nil {
		t.Fatalf("TryUnlock after %d failures = nil error, want ErrRateLimited", maxFailuresBeforeLockout)
	}

	// A different key must be unaffected - otherwise one noisy caller locks
	// the operator out, which is exactly what the shared unix-socket
	// RemoteAddr bucket did.
	if _, ok, err := g.TryUnlock("10.0.0.2", "hunter2"); !ok || err != nil {
		t.Fatalf("TryUnlock(other key, correct password) = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
}
