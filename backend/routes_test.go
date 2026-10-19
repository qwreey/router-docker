package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// TestCrossOriginWritesRefused is the regression test for the 2026-10-18
// review's N1: router-manager had no CSRF check, so a same-site sibling
// subdomain could POST to the admin API with the operator's unlock cookie.
func TestCrossOriginWritesRefused(t *testing.T) {
	old, oldLog := gate, containerLogPath
	gate = authgate.New("", "")
	containerLogPath = os.DevNull
	t.Cleanup(func() { gate, containerLogPath = old, oldLog })
	h := newHandler(t.TempDir(), t.TempDir())

	for _, tc := range []struct {
		name    string
		headers map[string]string
		refused bool
	}{
		{"same-site sibling subdomain", map[string]string{"Sec-Fetch-Site": "same-site"}, true},
		{"cross-site page", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
		{"Origin mismatch without Sec-Fetch-Site", map[string]string{"Origin": "https://app.example.com"}, true},
		{"router's own SPA", map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
		{"Origin matching Host", map[string]string{"Origin": "http://example.com"}, false},
		{"curl over the socket", nil, false},
	} {
		req := httptest.NewRequest(http.MethodPost, "http://example.com/api/tinyauth/users", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "text/plain")
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if refused := rec.Code == http.StatusForbidden; refused != tc.refused {
			t.Errorf("%s: POST /api/tinyauth/users = %d, want refused=%v", tc.name, rec.Code, tc.refused)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/auth/status", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("cross-site GET /api/auth/status = %d, want 200 - webmanager and code-server poll it", rec.Code)
	}
}
