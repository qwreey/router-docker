package main

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"router/internal/authgate"
)

// peerKey is the caller's transport-level peer address - the raw
// r.RemoteAddr with any port stripped. Meaningful only on a TCP listener;
// see rateLimitKey for what actually gets used as authgate's bucket key.
func peerKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clientKey is peerKey under its historical name, kept because
// handlers_vnc.go's realClientIP documents and uses it as its last-resort
// fallback.
func clientKey(r *http.Request) string { return peerKey(r) }

// rateLimitKey is the bucket authgate's per-client lockout counts against
// (see authgate.Gate.TryUnlock). Which value is correct depends entirely on
// what kind of socket router-manager is listening on, so main.go's listen()
// records that in listenerIsUnix and this is the only place that reads it:
//
//   - unix socket (the default, and every real deployment): r.RemoteAddr is
//     the socket's own peer and is byte-for-byte identical for every caller,
//     so keying on it gives ALL clients one shared lockout bucket. Five wrong
//     guesses from anything that can reach /router/ then locks out the
//     operator too - a trivially reachable permanent DoS on router's own
//     admin UI (2026-09-07 security review, finding H2). X-Real-IP is the
//     only thing that distinguishes callers here, and it is trustworthy
//     precisely because a unix socket has no route in except router's own
//     nginx, which sets that header unconditionally from $remote_addr on
//     every location that proxies to this socket (see
//     config/nginx/nginx.default.conf's /router/ and
//     nginx-service.default.sh's ROUTER_MANAGER_HOSTS block) rather than
//     passing through whatever a client sent. Same trust argument
//     handlers_vnc.go's realClientIP already makes, and it stands or falls
//     with the listener, not with this package.
//   - TCP (ROUTER_MANAGER_ADDR, the documented local-dev escape hatch): any
//     direct caller can set X-Real-IP to anything, so trusting it would let
//     an attacker reset their own lockout every request - strictly worse than
//     one shared bucket. r.RemoteAddr is both real and per-client there, so
//     use it.
//
// Deliberately does NOT fall back to X-Forwarded-For the way realClientIP
// does: that one is cosmetic (an IP column), this one decides whose
// lockout is whose, and XFF is the header an upstream appends to rather
// than overwrites.
func rateLimitKey(r *http.Request) string {
	if listenerIsUnix {
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
		// No X-Real-IP on a unix socket means something other than
		// router's own nginx is talking to it. Fall back to the shared
		// peer key: one bucket is bad, but it is at least not a bucket
		// an unknown caller gets to name.
		return peerKey(r)
	}
	return peerKey(r)
}

// handleAuthUnlock verifies a submitted password against the configured
// gate hash and, on success, issues an unlock cookie. Never itself wrapped
// in gate.RequirePassword — a locked-out client obviously needs to reach
// this to unlock.
func handleAuthUnlock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	token, ok, err := gate.TryUnlock(rateLimitKey(r), body.Password)
	if err != nil {
		if errors.Is(err, authgate.ErrRateLimited) {
			writeError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "incorrect password")
		return
	}

	gate.SetCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type authStatusResponse struct {
	Required bool `json:"required"`
	// Source is "env" (ROUTER_MANAGER_AUTH_PASSWORD_HASH pins it, /change
	// will refuse), "file" (set via /setup, changeable via /change), or
	// "unset" (nothing configured - frontend should show first-time setup,
	// not a change-password form).
	Source        string  `json:"source"`
	Unlocked      bool    `json:"unlocked"`
	UnlockedUntil *string `json:"unlockedUntil,omitempty"` // RFC3339, only set when Unlocked
	// TrustedHosts mirrors ROUTER_MANAGER_HOSTS (router/example-env.router)
	// - the dedicated-origin hostnames router's own nginx routes straight to
	// router-manager (see nginx.default.conf's NGINX_ROUTER_MANAGER_HOSTS
	// block). Read-only here: this is a static, restart-required nginx
	// routing/security boundary, same trust level as ALLOWED_HOSTS/
	// ALLOWED_EXPORT_HOSTS, so unlike the password it's never settable via
	// this API - the frontend just displays it so a user can tell at a
	// glance whether a dedicated domain is configured. Empty when unset.
	TrustedHosts []string `json:"trustedHosts"`
	// RequestHost is the Host header this specific request arrived on -
	// lets the frontend compare "am I currently on one of TrustedHosts" and
	// warn if not (see the localhost/shared-origin banner).
	RequestHost string `json:"requestHost"`
	// AppOrigin mirrors ROUTER_APP_ORIGIN - the origin router's own nginx
	// serves /app/ on, i.e. the *shared* hostname. Only meaningful when
	// TrustedHosts is non-empty: a dedicated ROUTER_MANAGER_HOSTS domain
	// deliberately serves router-manager and nothing else (that's the
	// whole point - no user-registered app content on router-manager's own
	// origin), so the VNC tab opened on that domain has no origin of its
	// own to load a viewer from and needs to be told the shared one. Empty
	// when unset, which is also the only correct value when there is no
	// dedicated domain at all - the SPA's own origin already serves /app/
	// then. Read-only here, same reasoning as TrustedHosts.
	AppOrigin string `json:"appOrigin"`
}

// appOrigin normalizes ROUTER_APP_ORIGIN down to a bare scheme://host[:port]
// and drops anything that isn't a plain http(s) origin, so the frontend
// never has to defend against a `javascript:`/`data:` value reaching an
// iframe src (it re-checks anyway - see useViewerOrigin.ts - but a value
// this side already refused can't get that far).
func appOrigin() string {
	raw := strings.TrimSpace(os.Getenv("ROUTER_APP_ORIGIN"))
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		log.Printf("auth: ignoring ROUTER_APP_ORIGIN=%q - expected a plain origin like https://code.example.com", raw)
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// trustedHosts parses ROUTER_MANAGER_HOSTS the same way
// nginx-service.default.sh does (comma-separated, trimmed, empties
// dropped) - kept in sync by hand since one is bash and the other is Go.
func trustedHosts() []string {
	raw := os.Getenv("ROUTER_MANAGER_HOSTS")
	if raw == "" {
		return []string{}
	}
	var hosts []string
	for _, h := range strings.Split(raw, ",") {
		h = strings.TrimSpace(h)
		if h != "" {
			hosts = append(hosts, h)
		}
	}
	if hosts == nil {
		hosts = []string{}
	}
	return hosts
}

// handleAuthStatus lets the frontend know whether to show a password
// prompt at all, and if so whether the current session already satisfies
// it.
func handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	resp := authStatusResponse{
		Required:     gate.Configured(),
		Source:       gate.Source(),
		TrustedHosts: trustedHosts(),
		RequestHost:  r.Host,
		AppOrigin:    appOrigin(),
	}
	if until, ok := gate.UnlockedUntil(r); ok {
		resp.Unlocked = true
		formatted := until.UTC().Format(time.RFC3339)
		resp.UnlockedUntil = &formatted
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAuthSetup sets the initial password - only works when nothing is
// configured yet (gate.Source() == "unset"). Never gated by
// RequirePassword, same reasoning as handleAuthUnlock: there's nothing to
// authenticate against until this succeeds once.
func handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := gate.SetupPassword(body.Password); err != nil {
		if errors.Is(err, authgate.ErrAlreadyConfigured) {
			writeError(w, http.StatusConflict, "password already configured - use /api/auth/change instead")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAuthChange replaces an already-configured password, requiring the
// current one. Not wrapped in RequirePassword either - it does its own
// explicit current-password check, the same self-contained pattern as
// handleAuthUnlock.
func handleAuthChange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.NewPassword == "" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := gate.ChangePassword(body.CurrentPassword, body.NewPassword); err != nil {
		switch {
		case errors.Is(err, authgate.ErrEnvPinned):
			writeError(w, http.StatusConflict, "password is set via ROUTER_MANAGER_AUTH_PASSWORD_HASH and can't be changed here")
		case errors.Is(err, authgate.ErrNotConfigured):
			writeError(w, http.StatusConflict, "no password configured yet - use /api/auth/setup instead")
		case errors.Is(err, authgate.ErrWrongPassword):
			writeError(w, http.StatusUnauthorized, "incorrect current password")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
