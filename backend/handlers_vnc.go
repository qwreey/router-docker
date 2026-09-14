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
	"sort"
	"strconv"
	"strings"
	"sync"
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

// realClientIP is clientKey's counterpart for the VNC connected-clients
// registry below: it prefers the client IP nginx hands over
// (X-Real-IP, then the first hop of X-Forwarded-For) and only falls back to
// clientKey(r) - the raw unix-socket peer address - when neither header is
// present.
//
// That fallback matters because clientKey(r) is USELESS here in the normal
// deployment: router-manager binds a unix socket by default
// (ROUTER_MANAGER_SOCK, see main.go's listen()), so r.RemoteAddr is the
// unix-socket peer on every single request, identical for every client
// regardless of who they actually are. Trusting the headers instead is
// safe specifically *because* the socket is unix-domain - the only process
// that can connect to it at all is router's own nginx (config/nginx/
// nginx.default.conf and nginx-service.default.sh's dedicated
// ROUTER_MANAGER_HOSTS block), and both locations now unconditionally
// overwrite X-Real-IP/X-Forwarded-For with $remote_addr/
// $proxy_add_x_forwarded_for rather than passing through whatever a
// client sent - so nothing reaching this handler can forge them.
//
// That trust boundary is the listener, not "this is router-manager's own
// code" - if ROUTER_MANAGER_ADDR is ever set to bind a TCP address instead
// (its documented opt-in escape hatch for local dev outside the container),
// this function would trust a header any direct TCP caller can set to
// whatever it wants, and would need revisiting before being relied on for
// anything more than a cosmetic IP column.
//
// authgate's own per-IP rate limiting had this exact same unix-socket blind
// spot and was deliberately left alone when this function was written; it's
// fixed now, in handlers_auth.go's rateLimitKey (2026-09-07 security review,
// finding H2). That one is NOT just a copy of this function, and the
// difference is deliberate: it branches on main.go's listenerIsUnix rather
// than on whether the header happens to be present, and it never falls back
// to X-Forwarded-For. A wrong IP here costs a misleading column in a UI
// panel; a caller-chosen key there would let an attacker reset their own
// lockout on every request, which is worse than the shared bucket it
// replaced.
func realClientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// The first entry is the original client; anything appended after
		// it is intermediate proxies (nginx's own $proxy_add_x_forwarded_for
		// appends to whatever it received, though there should be nothing
		// upstream of nginx here to have added one).
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	return clientKey(r)
}

// vncConn is one live BackendRFB client, tracked purely so the "연결된
// 클라이언트" panel (see docs/vnc.md) can list and disconnect them - see
// .claude/backlog/vnc-connected-clients.md in code-docker's own repo for
// why this only ever sees router-mediated clients, never a native client
// dialing the target's raw RFB port directly. Nothing in handleVncSocket's
// own bridge logic reads this; it exists only for the handlers below.
type vncConn struct {
	id          int64
	remoteIP    string
	userAgent   string
	connectedAt time.Time
	conn        net.Conn // the *websocket.NetConn wrapper - Close() unblocks the bridge's io.Copy exactly like a network failure would.
}

// vncClientInfo is the wire shape for GET .../clients. vncConn itself is
// never marshaled directly: its conn field must never leave this process,
// and keeping the two separate means that stays true even if vncConn grows
// more fields later.
type vncClientInfo struct {
	ID          int64  `json:"id"`
	RemoteIP    string `json:"remoteIp"`
	UserAgent   string `json:"userAgent"`
	ConnectedAt string `json:"connectedAt"`
}

// vncConnMu guards vncConnsByTarget and nextVncConnID - same
// package-level-mutex idiom internal/vnc already uses for the target
// registry itself.
var (
	vncConnMu        sync.Mutex
	vncConnsByTarget = map[string]map[int64]*vncConn{}
	nextVncConnID    int64
)

// registerVncConn records a newly-accepted client and returns its handle.
// Called right after the WebSocket upgrade in handleVncSocket, deregistered
// via that function's own defer.
func registerVncConn(target, remoteIP, userAgent string, conn net.Conn) *vncConn {
	vncConnMu.Lock()
	defer vncConnMu.Unlock()
	nextVncConnID++
	c := &vncConn{id: nextVncConnID, remoteIP: remoteIP, userAgent: userAgent, connectedAt: time.Now(), conn: conn}
	if vncConnsByTarget[target] == nil {
		vncConnsByTarget[target] = map[int64]*vncConn{}
	}
	vncConnsByTarget[target][c.id] = c
	return c
}

func deregisterVncConn(target string, id int64) {
	vncConnMu.Lock()
	defer vncConnMu.Unlock()
	m := vncConnsByTarget[target]
	delete(m, id)
	if len(m) == 0 {
		delete(vncConnsByTarget, target)
	}
}

// vncKickWindow is how long a client just disconnected via
// handleDeleteVncClient is refused a new connection to the same target.
// This exists because of a problem the connection registry alone doesn't
// solve: internal/vnc's novncQuery always sets reconnect=1, so a bare
// Close() on the stored conn makes the still-open noVNC tab reconnect
// almost immediately - the "끊기" button would appear to do nothing at all,
// since the same client is back in the list before the panel even
// refreshes. 15s is comfortably longer than noVNC's own reconnect backoff
// (a few seconds) but short enough that a legitimate reconnect after a real
// network blip isn't mistaken for the just-kicked client for long.
const vncKickWindow = 15 * time.Second

// vncKickKey identifies one target+client pairing for the kick window
// below. Keyed on IP *and* User-Agent, not IP alone: realClientIP is
// frequently shared by more than one browser (NAT, a corporate egress
// proxy, two people behind the same router) and keying on IP alone would
// disconnect all of them for vncKickWindow just because an operator meant
// to kick one. User-Agent is a cheap, already-available way to usually tell
// two different browsers apart without adding a cookie/fingerprint scheme
// just for this.
//
// This does NOT separate two tabs of the *same* browser on the same
// machine - they share both IP and User-Agent, so kicking one kicks both.
// That's deliberately fine: two same-browser tabs open on one target is
// exactly the "두 클라이언트가 desktop 크기를 두고 싸운다" case Vnc.tsx's own
// "새 창으로 옮기기" handoff exists to avoid, so treating them as one unit
// to kick together matches how this UI already treats them everywhere else.
type vncKickKey struct {
	target    string
	ip        string
	userAgent string
}

var (
	vncKickMu    sync.Mutex
	vncKickUntil = map[vncKickKey]time.Time{}
)

// kickVncClient records that this ip+userAgent pairing must not be allowed
// to reconnect to target until vncKickWindow has elapsed. Called from
// handleDeleteVncClient before it closes the connection, so the window is
// already in effect by the time the client's own reconnect logic notices
// the socket is gone.
func kickVncClient(target, ip, userAgent string) {
	vncKickMu.Lock()
	defer vncKickMu.Unlock()
	pruneVncKicksLocked()
	vncKickUntil[vncKickKey{target, ip, userAgent}] = time.Now().Add(vncKickWindow)
}

// vncClientKicked reports whether ip+userAgent is currently inside its kick
// window for target. Also prunes every expired entry while it's already
// holding the lock, rather than on a timer - this map is only ever touched
// on the connect/disconnect path, so there's no need for a background
// goroutine just to keep it from growing unbounded.
func vncClientKicked(target, ip, userAgent string) bool {
	vncKickMu.Lock()
	defer vncKickMu.Unlock()
	pruneVncKicksLocked()
	until, ok := vncKickUntil[vncKickKey{target, ip, userAgent}]
	return ok && time.Now().Before(until)
}

// pruneVncKicksLocked drops every entry whose window has already elapsed.
// Callers must hold vncKickMu.
func pruneVncKicksLocked() {
	now := time.Now()
	for k, until := range vncKickUntil {
		if now.After(until) {
			delete(vncKickUntil, k)
		}
	}
}

// handleListVncClients lists the clients currently bridged to name through
// this process - see the vncConn doc comment for what that does and
// doesn't cover. Read-only, so it isn't gate.RequirePassword'd, matching
// every other list endpoint in this file.
func handleListVncClients(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	vncConnMu.Lock()
	m := vncConnsByTarget[name]
	// Always a real (possibly empty) slice, never nil - json.Marshal would
	// otherwise send `null`, which every existing frontend list consumer in
	// this repo has to specifically guard against.
	list := make([]vncClientInfo, 0, len(m))
	for _, c := range m {
		list = append(list, vncClientInfo{
			ID:          c.id,
			RemoteIP:    c.remoteIP,
			UserAgent:   c.userAgent,
			ConnectedAt: c.connectedAt.UTC().Format(time.RFC3339),
		})
	}
	vncConnMu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	writeJSON(w, http.StatusOK, list)
}

// handleDeleteVncClient closes one client's bridged connection and starts
// its vncKickWindow. Gated like the socket route itself - disconnecting
// someone is at least as sensitive as connecting.
func handleDeleteVncClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid client id")
		return
	}
	vncConnMu.Lock()
	c, ok := vncConnsByTarget[name][id]
	vncConnMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "no such client")
		return
	}
	// Recorded before Close(): see vncKickWindow's doc comment for why the
	// window has to already be in effect before the client's own socket
	// actually goes away.
	kickVncClient(name, c.remoteIP, c.userAgent)
	_ = c.conn.Close()
	log.Printf("vnc: %s client #%d (%s) disconnected by operator, refusing reconnects from that ip+user-agent for %s", name, id, c.remoteIP, vncKickWindow)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

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

	// See realClientIP's own doc comment for why this - and not clientKey,
	// which is always the same unix-socket peer address here - is what the
	// kick window and the "connected clients" panel need.
	remoteIP := realClientIP(r)
	if vncClientKicked(name, remoteIP, r.UserAgent()) {
		// Before the upgrade, same as the dial-failure response below - the
		// browser's network tab shows a real reason instead of a socket
		// that just closes.
		writeError(w, http.StatusForbidden, "disconnected from this vnc target by an operator - wait a few seconds before reconnecting")
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

	// Registered after the upgrade (sock is what a disconnect handler
	// actually closes) and deregistered on the way out regardless of why
	// the bridge ended - a target restart or a network blip must clear this
	// client from the panel exactly as promptly as an operator-initiated
	// disconnect does.
	vc := registerVncConn(name, remoteIP, r.UserAgent(), sock)
	defer deregisterVncConn(name, vc.id)

	log.Printf("vnc: %s -> %s connected (client #%d, %s)", name, t.Target, vc.id, remoteIP)
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
	log.Printf("vnc: %s -> %s closed (client #%d)", name, t.Target, vc.id)
}
