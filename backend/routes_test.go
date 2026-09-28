package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"router/internal/authgate"
)

// TestAdminReadsAreGated is the regression test for the 2026-09-16 audit's
// F18: these GETs were registered without RequirePassword, so anyone who
// could reach /router/ could list forwards, outbound rules, DNS overrides,
// tinyauth usernames and the IP/User-Agent of whoever was watching a VNC
// session. With no password configured the gate answers 503 before any
// handler runs, which is what this checks.
func TestAdminReadsAreGated(t *testing.T) {
	old, oldLog := gate, containerLogPath
	gate = authgate.New("", "")
	containerLogPath = os.DevNull // /api/auth/status announces the setup token
	t.Cleanup(func() { gate, containerLogPath = old, oldLog })
	mux := newMux(t.TempDir(), t.TempDir())

	for _, path := range []string{
		"/api/tailscale/config",
		"/api/tailscale/forwards",
		"/api/tailscale/publish",
		"/api/tailscale/status",
		"/api/dev-proxy/exposes",
		"/api/app-routes/apps",
		"/api/vnc/targets",
		"/api/vnc/targets/x/clients",
		"/api/tinyauth/users",
		"/api/dns/blocklist-sources",
		"/api/dns/blocklist-sources/builtin/status",
		"/api/dns/custom-hosts",
		"/api/dns/resolver",
		"/api/dns/query?name=example.com",
		"/api/netgate/outbound",
		"/api/netgate/forwards",
		"/api/netgate/bandwidth",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s = %d, want 503 from the gate", path, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/auth/status = %d, want 200 - it is how the SPA finds the setup form", rec.Code)
	}
}
