// Ported from webmanager/backend/handlers_tailscale.go (see router/plan.md's
// TODO list) — same route set, adapted to this file's decode→call→
// writeJSON/writeError style (see handlers_devproxy.go). No host port is
// published for router-manager (see main.go), so these endpoints are
// private-by-default the same way GET /api/tailscale/state already is. On
// top of that, every mutating route here is wrapped in main.go's package-
// level `gate` (internal/authgate, opt-in via ROUTER_MANAGER_AUTH_PASSWORD_HASH)
// — reads stay open, writes require an unlock.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"router/internal/supervisor"
	"router/internal/tailscale"
)

var (
	supervisorClient = supervisor.NewClient("/run/supervisor.sock")
	tailscaleLogin   = tailscale.NewLoginManager()
)

func restartSupervisorProgram(ctx context.Context, name string) error {
	if err := supervisorClient.StopProcess(ctx, name); err != nil {
		var f *supervisor.Fault
		if !errors.As(err, &f) || f.Code != 70 { // NOT_RUNNING is expected when already stopped
			return err
		}
	}
	if err := supervisorClient.StartProcess(ctx, name); err != nil {
		return withProgramLogTail(ctx, name, err)
	}
	return nil
}

// programLogTailBytes is how much of a failed program's log to look at - a
// negative offset with a zero length is supervisord's own "tail the file"
// form (see supervisor's readFile).
const programLogTailBytes = 2000

// withProgramLogTail explains a failed start with the program's own last
// words. supervisord reports nothing but `SPAWN_ERROR: <name>`, which says
// only "it died before startsecs" and never why; the actual reason is always
// in the program's own stdout/stderr (every config/supervisord.d/*.conf
// captures both under /var/log/<name>/). Without this the UI shows a bare
// fault code and the message that identifies the problem - e.g. tinyauth's
// own `failed to load users: invalid user format` - is only reachable by
// docker exec'ing into the container, which is exactly how the tinyauth user
// CRUD bug this was added for had to be diagnosed.
func withProgramLogTail(ctx context.Context, name string, startErr error) error {
	tails := make([]string, 0, 2)
	for _, read := range []func(context.Context, string, int, int) (string, error){
		supervisorClient.ReadProcessStderrLog,
		supervisorClient.ReadProcessStdoutLog,
	} {
		out, err := read(ctx, name, -programLogTailBytes, 0)
		if err != nil { // no log file configured/written yet - nothing to add
			continue
		}
		if tail := lastLogLines(out, 3); tail != "" {
			tails = append(tails, tail)
		}
	}
	if len(tails) == 0 {
		return startErr
	}
	return fmt.Errorf("%w (%s log: %s)", startErr, name, strings.Join(tails, " | "))
}

// lastLogLines flattens the last n non-empty lines of a log tail into one
// line - this ends up inside a JSON error field a banner renders. Terminal
// color is already gone by here (internal/supervisor's own sanitizeXML has
// to strip it before the response can even be parsed).
func lastLogLines(s string, n int) string {
	lines := make([]string, 0, n)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		// Drop blanks, and collapse a repeat of the line before it: a
		// program supervisord retried three times says the same thing three
		// times, which would fill the whole tail with one message.
		if line == "" || (len(lines) > 0 && lines[len(lines)-1] == line) {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// restartTailscaleForward restarts the tailscale-forward program (see
// router/config/supervisord.d/tailscale.conf) — it reads ConfigPath's
// socks5_address/retry_intervall/forwards once at startup, so any mutation
// to those fields needs this to take effect.
func restartTailscaleForward(ctx context.Context) error {
	return restartSupervisorProgram(ctx, "tailscale-forward")
}

// restartTailscalePublish restarts the tailscale-publish program — it reads
// ConfigPath's publish list once at startup (and resets/reapplies `tailscale
// serve` rules from it), so any mutation to publish entries needs this to
// take effect.
func restartTailscalePublish(ctx context.Context) error {
	return restartSupervisorProgram(ctx, "tailscale-publish")
}

// writeSupervisorErr responds to a supervisord restart failure that happens
// AFTER the underlying config change was already persisted successfully -
// every caller only reaches this after its own mutation (dns.CreateSource,
// tailscale.AddForward, tinyauthusers.AddUser, etc.) already wrote to disk.
// Without the explicit "saved" field and clarified message, a client can't
// tell "nothing happened, safe to retry" from "the change is saved but the
// affected program failed to restart, so it may not be live yet" - blindly
// retrying the latter can spuriously 409 on a create that already
// succeeded. See root CLAUDE.md's code-quality audit.
func writeSupervisorErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": "change saved, but restarting the affected program failed: " + err.Error(),
		"saved": true,
	})
}

// tailscaleConfigResponse wraps GlobalConfig with LoginServerPinned so the
// frontend can render the login-server field read-only with an explanatory
// note the same way TinyauthUsers' `pinned` already does for
// TINYAUTH_AUTH_USERS - see tailscale.GlobalConfig's own doc comment for the
// env-always-wins priority this reflects.
type tailscaleConfigResponse struct {
	tailscale.GlobalConfig
	LoginServerPinned bool `json:"loginServerPinned"`
}

func handleGetTailscaleConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := tailscale.GetGlobalConfig(tailscale.ConfigPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pinned := tailscale.LoginServerPinned()
	if pinned {
		// Report the value actually in effect, not whatever (stale/unset)
		// value happens to be sitting in the file - same idea as
		// EffectiveLoginServer, inlined here since we already know it's
		// pinned.
		cfg.LoginServer = os.Getenv("TAILSCALE_LOGIN_SERVER")
	}
	writeJSON(w, http.StatusOK, tailscaleConfigResponse{GlobalConfig: cfg, LoginServerPinned: pinned})
}

func handlePutTailscaleConfig(w http.ResponseWriter, r *http.Request) {
	var body tailscale.GlobalConfig
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	pinned := tailscale.LoginServerPinned()
	if pinned {
		// TAILSCALE_LOGIN_SERVER always wins - never let a client-submitted
		// value (the disabled UI field just echoes the env value back, but
		// don't rely on that) overwrite what's on disk. socksAddress/
		// retryInterval remain editable regardless of this pin.
		existing, err := tailscale.GetGlobalConfig(tailscale.ConfigPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		body.LoginServer = existing.LoginServer
	}
	if err := tailscale.SetGlobalConfig(tailscale.ConfigPath, body); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, tailscale.ErrValidation) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	if err := restartTailscaleForward(r.Context()); err != nil {
		writeSupervisorErr(w, err)
		return
	}
	if pinned {
		body.LoginServer = os.Getenv("TAILSCALE_LOGIN_SERVER")
	}
	writeJSON(w, http.StatusOK, tailscaleConfigResponse{GlobalConfig: body, LoginServerPinned: pinned})
}

func handleListTailscaleForwards(w http.ResponseWriter, r *http.Request) {
	forwards, err := tailscale.ListForwards(tailscale.ConfigPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, forwards)
}

func handleAddTailscaleForward(w http.ResponseWriter, r *http.Request) {
	var body tailscale.Forward
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	forward, err := tailscale.AddForward(tailscale.ConfigPath, body)
	if err != nil {
		if errors.Is(err, tailscale.ErrForwardExists) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		status := http.StatusInternalServerError
		if errors.Is(err, tailscale.ErrValidation) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	if err := restartTailscaleForward(r.Context()); err != nil {
		writeSupervisorErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, forward)
}

func handleUpdateTailscaleForward(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body tailscale.Forward
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	forward, err := tailscale.UpdateForward(tailscale.ConfigPath, name, body)
	if err != nil {
		if errors.Is(err, tailscale.ErrForwardNotFound) {
			writeError(w, http.StatusNotFound, "forward not found")
			return
		}
		status := http.StatusInternalServerError
		if errors.Is(err, tailscale.ErrValidation) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	if err := restartTailscaleForward(r.Context()); err != nil {
		writeSupervisorErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, forward)
}

func handleDeleteTailscaleForward(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := tailscale.DeleteForward(tailscale.ConfigPath, name); err != nil {
		if errors.Is(err, tailscale.ErrForwardNotFound) {
			writeError(w, http.StatusNotFound, "forward not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := restartTailscaleForward(r.Context()); err != nil {
		writeSupervisorErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleListTailscalePublish(w http.ResponseWriter, r *http.Request) {
	publish, err := tailscale.ListPublish(tailscale.ConfigPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, publish)
}

func handleAddTailscalePublish(w http.ResponseWriter, r *http.Request) {
	var body tailscale.Publish
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	publish, err := tailscale.AddPublish(tailscale.ConfigPath, body)
	if err != nil {
		if errors.Is(err, tailscale.ErrPublishExists) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		status := http.StatusInternalServerError
		if errors.Is(err, tailscale.ErrValidation) || errors.Is(err, tailscale.ErrInvalidMode) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	if err := restartTailscalePublish(r.Context()); err != nil {
		writeSupervisorErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, publish)
}

func handleUpdateTailscalePublish(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body tailscale.Publish
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	publish, err := tailscale.UpdatePublish(tailscale.ConfigPath, name, body)
	if err != nil {
		if errors.Is(err, tailscale.ErrPublishNotFound) {
			writeError(w, http.StatusNotFound, "publish not found")
			return
		}
		status := http.StatusInternalServerError
		if errors.Is(err, tailscale.ErrValidation) || errors.Is(err, tailscale.ErrInvalidMode) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	if err := restartTailscalePublish(r.Context()); err != nil {
		writeSupervisorErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publish)
}

func handleDeleteTailscalePublish(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := tailscale.DeletePublish(tailscale.ConfigPath, name); err != nil {
		if errors.Is(err, tailscale.ErrPublishNotFound) {
			writeError(w, http.StatusNotFound, "publish not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := restartTailscalePublish(r.Context()); err != nil {
		writeSupervisorErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// tailscaleStatusResponse is GET /api/tailscale/status's body. Status is a
// plain pointer with no `omitempty` so it serializes as an explicit JSON
// `null` when Available is false, matching the read-only status contract
// the frontend expects. Enabled reflects TAILSCALE_ENABLED (tailscale.
// Enabled()) independent of Available, so a caller can tell "deliberately
// turned off" apart from "on but daemon not ready/logged in yet".
//
// That Enabled bool is also on GET /api/tailscale/state, which is where the
// sidebars read it from now — this route went behind the password gate in
// the 2026-09-07 security review (it hands out the whole tailnet peer list),
// and a background sidebar poll must not sit on a gated route. router's own
// frontend/src/components/Tailscale/useTailscaleEnabled.ts was moved to
// /state accordingly, and so was code-docker's webmanager copy of that hook
// (webmanager/frontend/src/components/RouterEmbed/useTailscaleEnabled.ts) —
// left on this route it would have swallowed the 401/503 into "enabled" and
// kept webmanager's Tailscale tab visible even with TAILSCALE_ENABLED=false.
type tailscaleStatusResponse struct {
	Available bool              `json:"available"`
	Enabled   bool              `json:"enabled"`
	Status    *tailscale.Status `json:"status,omitempty"`
}

// handleTailscaleStatus reports live tailnet status via `tailscale status
// --json`: `tailscale` missing, a stopped daemon, or a parse failure are all
// normal states here, not errors — never a non-200 for any of them. This is
// the detailed status (self/peers) the Tailscale tab renders — distinct from
// GET /api/tailscale/state's minimal {backendState, authUrl} shape, which
// code-server's sign-in banner polls instead.
//
// Short-circuits on TAILSCALE_ENABLED=false before touching GetStatus, same
// as GetState already does — tailscaled was never started in that case, so
// the exec would otherwise sit for up to GetStatus's own 5s timeout waiting
// on a socket that's never going to answer, on every poll that reaches here.
//
// Behind gate.RequirePassword (see main.go) — the one GET besides the VNC
// bridge that is, because the peer list it returns is a description of the
// operator's private network, not a status flag.
func handleTailscaleStatus(w http.ResponseWriter, r *http.Request) {
	if !tailscale.Enabled() {
		writeJSON(w, http.StatusOK, tailscaleStatusResponse{Available: false, Enabled: false})
		return
	}
	status, err := tailscale.GetStatus(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, tailscaleStatusResponse{Available: false, Enabled: true})
		return
	}
	writeJSON(w, http.StatusOK, tailscaleStatusResponse{Available: true, Enabled: true, Status: &status})
}

// tailscaleLoginStartRequest is POST /api/tailscale/login/start's optional
// JSON body - an empty/absent body is treated as {"forceReauth": false},
// preserving the original first-time-login behavior for existing callers.
type tailscaleLoginStartRequest struct {
	ForceReauth bool `json:"forceReauth"`
}

// handleTailscaleLoginStart triggers an on-demand `tailscale up`, for the
// Tailscale tab's login retry button - the automatic attempt
// tailscale-service.default.sh makes on first boot only fires once ever (see
// its LOGIN_ATTEMPTED_MARKER), so this is how a later retry happens without
// needing a container restart.
//
// Deliberately checks current status first and skips starting a second
// process if a login is already pending (AuthURL set) - the frontend should
// just keep polling the existing GET /api/tailscale/status instead, which
// already reports BackendState/AuthURL without any stdout scraping. That
// same check would otherwise reject a re-authentication request outright
// (BackendState == "Running" looks identical to "already logged in, nothing
// to do") - ForceReauth skips it and passes --force-reauth through to
// LoginManager.Start, which is what makes `tailscale up` produce a fresh
// AuthURL instead of being a no-op against an already-authenticated backend.
//
// The login server itself always comes from tailscale.EffectiveLoginServer
// (TAILSCALE_LOGIN_SERVER env wins, else the Tailscale tab's own 기본 설정)
// - TAILSCALE_HOSTNAME remains env-only (see GlobalConfig's own doc comment
// on why only the login server got a UI counterpart).
func handleTailscaleLoginStart(w http.ResponseWriter, r *http.Request) {
	var req tailscaleLoginStartRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // no body / malformed body -> zero value (first-login behavior)
	}

	if !req.ForceReauth {
		if status, err := tailscale.GetStatus(r.Context()); err == nil {
			if status.BackendState == "Running" {
				writeError(w, http.StatusConflict, "already logged in")
				return
			}
			if status.AuthURL != "" {
				writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
				return
			}
		}
	}

	loginServer, err := tailscale.EffectiveLoginServer(tailscale.ConfigPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	hostname := os.Getenv("TAILSCALE_HOSTNAME")
	if err := tailscaleLogin.Start(loginServer, hostname, req.ForceReauth); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTailscaleLoginCancel kills an in-flight on-demand login attempt, if
// any. Always 200 - idempotent, matching LoginManager.Cancel's contract.
func handleTailscaleLoginCancel(w http.ResponseWriter, r *http.Request) {
	_ = tailscaleLogin.Cancel()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
