// The VNC tab's HTTP layer - same conventions as handlers_approutes.go/
// handlers_devproxy.go (reads open, gate.RequirePassword on every mutating
// route). Thin on purpose: internal/vnc owns the App-Route-in-lockstep
// bookkeeping, this file only maps it onto HTTP.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"router/internal/approutes"
	"router/internal/vnc"
)

// vncTargetsResponse ships the backend picker's options alongside the list
// itself, so the frontend doesn't hand-duplicate a list only the backend
// actually knows (see internal/vnc.Backends - Selkies is meant to land
// there and show up in the UI without a matching frontend change).
type vncTargetsResponse struct {
	Targets  []vnc.Info `json:"targets"`
	Backends []string   `json:"backends"`
}

func handleListVncTargets(w http.ResponseWriter, r *http.Request) {
	list, err := vnc.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, vncTargetsResponse{Targets: list, Backends: vnc.Backends()})
}

func handleCreateVncTarget(w http.ResponseWriter, r *http.Request) {
	var body vnc.Target
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := vnc.Create(r.Context(), body); err != nil {
		writeVncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func handleUpdateVncTarget(w http.ResponseWriter, r *http.Request) {
	oldName := r.PathValue("name")
	var body vnc.Target
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// An omitted name means "keep this one" rather than "rename to empty" -
	// same contract handleUpdateAppRoute already uses.
	if body.Name == "" {
		body.Name = oldName
	}
	if err := vnc.Update(r.Context(), oldName, body); err != nil {
		writeVncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func handleDeleteVncTarget(w http.ResponseWriter, r *http.Request) {
	if err := vnc.Delete(r.Context(), r.PathValue("name")); err != nil {
		writeVncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeVncError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vnc.ErrTargetExists), errors.Is(err, approutes.ErrAppExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, vnc.ErrTargetNotFound), errors.Is(err, approutes.ErrAppNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, approutes.ErrReloadFailed):
		// The fragment and the registry are both already written - this is
		// an apply failure, not bad client input. Same split
		// writeAppRouteError makes.
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

// dialTimeout bounds the connect to the target's RFB port. Short on
// purpose: both ends are on a Docker network router is already attached to,
// so anything slower than this is a target that's down, not a slow link -
// and noVNC's own reconnect loop will try again.
const dialTimeout = 10 * time.Second

// handleVncSocket is the transport half of BackendRFB: it bridges the
// browser's WebSocket to the target's raw RFB port, which is exactly what
// websockify does for a target that hosts its own noVNC. Doing it here
// instead is what makes the viewer first-party - same origin as the SPA,
// gated by router-manager's own lock, no App Route and no tinyauth in the
// path. See internal/vnc's package doc comment.
//
// The target is re-read from the store per connection rather than captured
// anywhere, so editing a target's address takes effect on the next connect.
func handleVncSocket(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	t, err := vnc.Get(name)
	if err != nil {
		if errors.Is(err, vnc.ErrTargetNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if t.Backend != vnc.BackendRFB {
		// A BackendNoVNC target is reached through its App Route instead;
		// its own websockify is the bridge. Answering here would connect
		// this socket to that target's *web* port and speak RFB at an HTTP
		// server, which fails in a thoroughly unhelpful way.
		writeError(w, http.StatusBadRequest, "vnc target "+name+" uses the "+t.Backend+" backend, which is proxied through its App Route rather than this socket")
		return
	}
	// Re-validated per connection, not just at save time: the allowlist is
	// read from the environment at process start, so a target saved while
	// ROUTER_EXTRA_ALLOWED_TARGET_HOSTS still named its host must not keep
	// working as a dial-anywhere hole after that host was removed from it.
	if err := approutes.ValidateTarget(t.Target); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	upstream, err := net.DialTimeout("tcp", t.Target, dialTimeout)
	if err != nil {
		// Before the upgrade, so this is still a plain HTTP response the
		// browser's network tab will show - noVNC would otherwise just
		// report a closed socket with no reason.
		writeError(w, http.StatusBadGateway, "couldn't reach "+t.Target+": "+err.Error())
		return
	}
	defer upstream.Close()

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		// Accept has already written its own response.
		log.Printf("vnc: websocket accept for %q: %v", name, err)
		return
	}
	// Not r.Context(): the request context is cancelled once ServeHTTP
	// returns, and NetConn's own goroutines outlive the last Read here.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer c.CloseNow()

	// -1 disables the default per-message read limit. RFB client messages
	// are small, but a clipboard paste is one message of unbounded size and
	// there is no reason for this bridge to have an opinion about it.
	c.SetReadLimit(-1)
	// MessageBinary: RFB is a byte stream, and noVNC sets binaryType
	// "arraybuffer" and never requests a subprotocol (core/rfb.js's
	// _wsProtocols defaults to []), so Accept must not negotiate one either.
	sock := websocket.NetConn(ctx, c, websocket.MessageBinary)

	log.Printf("vnc: %s -> %s connected", name, t.Target)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(upstream, sock)
		// Half-close so the target sees the client go away instead of
		// waiting on a socket nobody will write to again.
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	_, _ = io.Copy(sock, upstream)
	cancel()
	<-done
	log.Printf("vnc: %s -> %s closed", name, t.Target)
}
