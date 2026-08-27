// Package vnc manages the VNC tab's own target registry — the "웹 대시보드에
// 라이브 임베드" half of .claude/backlog/router-vnc-tab-plan.md's 경로 B,
// whose transport half (noVNC+websockify in front of an unchanged wayvnc,
// plus targetguard.ExtraAllowedHostsEnv so router is allowed to reach a
// sibling project's VNC container at all) already shipped.
//
// Backend is the axis this package is organized around, and it decides
// *who serves the viewer*:
//
//   - BackendRFB (the default) — router serves noVNC itself, from its own
//     origin, and bridges the browser's WebSocket to the target's **raw RFB
//     port** (handleVncSocket in backend/handlers_vnc.go). Nothing about
//     the target is web-facing: it only has to speak RFB, the same thing a
//     native client like TigerVNC connects to. No App Route is involved.
//   - BackendNoVNC — the target runs its own web VNC front end
//     (noVNC+websockify, typically :6080) and router only reverse-proxies
//     it, via an App Routes fragment kept in lockstep with this registry.
//
// BackendNoVNC came first and was, for a while, the only shape. It works,
// but it makes a first-party router feature indistinguishable from a
// user-registered third-party app, and that leaked in ways that had to be
// worked around one at a time: the viewer couldn't be shown on a dedicated
// ROUTER_MANAGER_HOSTS domain at all (/app/ deliberately isn't served
// there, hence ROUTER_APP_ORIGIN), gating it needed the whole tinyauth
// forward-auth path rather than router-manager's own lock, every target
// container had to ship a web VNC stack of its own, and noVNC's
// request-a-0x0-desktop bug had to be patched separately in each of them.
// VNC is a protocol — a layer, like DNS or TCP — not an app, so router
// owning the client side is the shape that matches. BackendRFB is that,
// and a future RDP backend belongs on the same axis.
//
// BackendNoVNC is kept, not deprecated: a target whose front end is *not*
// noVNC (Selkies, which captures the target's own compositor and can't be
// moved into router — see the plan's 결정 사항, "향후 방향은 noVNC와
// Selkies를 나란히 지원") has no raw RFB port to bridge, and proxying its
// web front end is the only thing that can work. So the two are genuinely
// different transports, not an old and a new way to do one thing.
//
// What this package owns either way is the per-target metadata a viewer
// needs but a reverse proxy doesn't (a human Label, the Backend, and
// ResizeMode), persisted in StorePath. For BackendNoVNC it additionally
// keeps a matching approutes fragment in lockstep, so registering a target
// is one action instead of "add an App Route, then remember its name in the
// VNC tab" — and it is deliberately NOT a second source of truth for that
// proxying: List re-reads approutes every call and reports drift
// (RouteMissing/RouteDiverged) rather than caching what it wrote, so an app
// fragment deleted or hand-edited from the App Routes tab shows up as a
// warning in the VNC tab instead of a viewer that silently 404s.
package vnc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"

	"router/internal/approutes"
	"router/internal/atomicfile"
	"router/internal/devproxy"
)

// StorePath holds the target registry. Under the same
// /var/lib/code-docker-router state root every other web-editable router
// config lives in (netgate.LiveConfigPath, authgate's hash file, ...), so a
// single ROUTER_VOLUME bind mount persists it with the rest.
const StorePath = "/var/lib/code-docker-router/vnc/targets.json"

// mu serializes every read-modify-write below — same reasoning as
// internal/netgate's own package-level mutex: two browser tabs saving at
// once would otherwise both read the same pre-mutation list and the second
// writer would silently drop the first's change.
var mu sync.Mutex

var (
	ErrTargetExists   = errors.New("vnc target already exists")
	ErrTargetNotFound = errors.New("vnc target not found")
	ErrValidation     = errors.New("invalid vnc target")
)

const (
	// BackendRFB — router serves the viewer and bridges to the target's raw
	// RFB port itself. Target.Target is that raw port (wayvnc's own :5900,
	// the one a native client uses), NOT a web port.
	BackendRFB = "rfb"
	// BackendNoVNC — the target serves its own web VNC front end and router
	// only reverse-proxies it through App Routes. Target.Target is that web
	// port (websockify's :6080). See the package doc comment for why both
	// exist.
	BackendNoVNC = "novnc"
)

// backend describes one Backend's transport. Adding RDP later (router-side,
// so appRoute:false) is one more entry plus its own bridge handler.
type backend struct {
	// appRoute: this backend's transport is an App Routes fragment kept in
	// lockstep with the registry, rather than router's own viewer.
	appRoute bool
	// viewer builds the origin-relative viewer URL.
	viewer func(t Target) string
}

// backendOrder is the order the frontend's picker shows these in, and its
// first entry is what a new target defaults to — so it is deliberately NOT
// sorted (BackendRFB is the one that should be reached for).
var backendOrder = []string{BackendRFB, BackendNoVNC}

var backends = map[string]backend{
	BackendRFB:   {appRoute: false, viewer: rfbViewerPath},
	BackendNoVNC: {appRoute: true, viewer: appViewerPath},
}

// novncQuery is the part of noVNC's own query string that's a property of
// the viewer rather than of the target: autoconnect makes the embed connect
// without a click (the whole point of a dashboard tab), and reconnect
// survives a target container restart without the user re-opening the tab.
// resize is per-target (see Target.ResizeMode) and added by each builder.
func novncQuery(t Target) url.Values {
	q := url.Values{}
	q.Set("autoconnect", "1")
	q.Set("reconnect", "1")
	q.Set("resize", resizeMode(t))
	return q
}

// ViewerBasePath is where router-manager serves its own copy of noVNC (see
// backend/main.go's /novnc/ route). Two things about this string are
// deliberate:
//
//   - the /router/ prefix, because that's the one path shape that works
//     from *both* deployments — the shared hostname's `location /router/`
//     and a dedicated ROUTER_MANAGER_HOSTS domain, whose nginx block
//     carries the same /router/ location precisely so origin-independent
//     links like this one keep working (see
//     config/nginx/nginx-service.default.sh).
//   - "novnc", not "vnc". The SPA's own VNC tab is at /router/vnc, and a
//     static mount at /router/vnc/ shadows it: Go's ServeMux installs an
//     implicit /vnc -> /vnc/ redirect for a pattern ending in a slash, and
//     since nginx has already stripped /router by then, that redirect sends
//     the browser to /vnc/ at the *origin root* - out of the SPA entirely,
//     onto a 404. Confirmed live before this name was picked.
const ViewerBasePath = "/router/novnc/"

// rfbViewerPath builds the URL for router's own noVNC against a target's
// raw RFB port.
//
// Two parameters here are load-bearing and easy to get wrong:
//
//   - host= is set to the empty string on purpose. noVNC builds its
//     WebSocket URL from `host`/`port` when host is truthy and from
//     `path` resolved against location.href when it isn't (app/ui.js's
//     connect()). We want the second branch — it's the only one that
//     inherits the browser's own scheme/host/port through whatever outer
//     reverse proxy is in front. host defaults to "" already, but noVNC
//     *persists* settings per origin, so a value someone once typed into
//     the connect panel would otherwise stick; an explicit query parameter
//     beats stored settings and pins the branch.
//   - path is relative with a leading "../" rather than root-absolute.
//     The viewer page is at <prefix>/vnc/vnc.html and the socket at
//     <prefix>/api/..., where <prefix> is /router on both deployments
//     above — but nginx strips it before router-manager ever sees a path,
//     so a root-absolute value would have to hardcode the prefix the
//     *browser* uses. Letting the browser resolve ".." against its own
//     location.href gets that right by construction.
func rfbViewerPath(t Target) string {
	q := novncQuery(t)
	q.Set("host", "")
	q.Set("path", "../api/vnc/targets/"+url.PathEscape(t.Name)+"/ws")
	return ViewerBasePath + "vnc.html?" + q.Encode()
}

// appViewerPath builds the URL for a target that serves its own noVNC,
// reached through its App Route.
//
// Notably absent: noVNC's `path` setting — websockify accepts the WebSocket
// upgrade on any path and noVNC resolves its default (`websockify`)
// relative to the page's own location, which already lands inside
// /app/<name>/ and back through Caddy's handle_path to the same target.
// That was confirmed live in the plan's own e2e run ("WebSocket
// 업그레이드+RFB 배너 수신"), so pinning an absolute path here would only
// break it.
func appViewerPath(t Target) string {
	return "/app/" + url.PathEscape(t.Name) + "/vnc.html?" + novncQuery(t).Encode()
}

// Resize modes, spelled exactly as noVNC's own `resize` setting so
// viewerPath can pass the value straight through instead of translating it.
//
//   - ResizeRemote asks the *server* to make its desktop the size of the
//     browser window (the RFB SetDesktopSize extension — what TigerVNC does
//     when you drag its window edge). wayvnc has this enabled by default
//     (`-R/--disable-resizing` is the opt-out) and its headless output
//     follows live, verified against a real target: connecting at a
//     960x634 viewport moved HEADLESS-1 off 1920x1080 to match, and it
//     tracked further window resizes.
//   - ResizeScale keeps the remote desktop's own size and fits the received
//     framebuffer into the iframe instead. This was the hardcoded behavior
//     before ResizeMode existed.
//   - ResizeOff does neither.
//
// The default is ResizeRemote, including for targets stored before this
// field existed (empty string — see resizeMode). It is deliberately not
// forced viewer-wide, because it isn't universally safe: a server with no
// SetDesktopSize support (x11vnc in front of a fixed-size Xvfb, say) simply
// refuses the request, and noVNC then neither resizes nor scales — the
// framebuffer keeps its own size and the viewer grows scrollbars. Those
// targets want ResizeScale, hence per-target.
//
// One more thing worth knowing before picking ResizeRemote for a heavy
// target: noVNC requests the size in *device* pixels, so a HiDPI browser
// asks for a proportionally larger desktop (a 1400px-wide window at DPR 1.2
// requested 1680px, measured), which is that much more for the target to
// render and encode.
const (
	ResizeRemote = "remote"
	ResizeScale  = "scale"
	ResizeOff    = "off"
)

// resizeModes is the validation set for the above. Unlike Backends it is
// NOT shipped to the frontend: a mode only exists here if some viewer's own
// query parameter accepts it, so it's a closed set the picker can spell out
// with proper labels rather than a list the server can grow on its own.
var resizeModes = map[string]struct{}{
	ResizeRemote: {},
	ResizeScale:  {},
	ResizeOff:    {},
}

// resizeMode resolves t's mode, mapping "" (a target stored before the
// field existed) onto the default. Empty is kept as a real state rather
// than normalized away on save, so those targets follow the default if it
// ever changes instead of being frozen at whatever it was when they were
// last edited.
func resizeMode(t Target) string {
	if t.ResizeMode == "" {
		return ResizeRemote
	}
	return t.ResizeMode
}

// Backends lists every implemented backend, for the tab's own picker, in
// the order it should offer them - see backendOrder.
func Backends() []string {
	return append([]string(nil), backendOrder...)
}

// Target is one registered VNC target. Name is the registry key and, for a
// BackendNoVNC target, doubles as the App Route name and therefore the
// /app/<name>/ path segment - so it carries devproxy.ValidateName's
// RFC1123-label constraint either way (a BackendRFB target's name also ends
// up in a URL path, via rfbViewerPath). Label is display-only.
//
// What Target.Target means depends on Backend, and getting it backwards is
// the single most likely mistake here: BackendRFB wants the **raw RFB**
// port (wayvnc's own :5900, what TigerVNC connects to), BackendNoVNC wants
// the target's **web** VNC port (websockify's :6080). See the package doc
// comment; the tab's own dialog warns on a port that looks wrong for the
// selected backend.
//
// ResizeMode is one of the resize constants above (or "" for the default) —
// the only target field that changes the viewer URL rather than the App
// Route, so it's the one thing here approutes never sees.
type Target struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Target      string `json:"target"`
	Backend     string `json:"backend"`
	RequireAuth bool   `json:"requireAuth"`
	ResizeMode  string `json:"resizeMode"`
}

// Info is what List returns: the stored target plus live drift detection
// against the App Route that actually carries it.
type Info struct {
	Target
	// ViewerPath is the origin-relative URL of the viewer page. The
	// frontend can't just build this itself, because which origin it's
	// relative TO is not always the one the SPA is served from — see
	// ViewerOrigin and components/Vnc/useViewerOrigin.ts.
	ViewerPath string `json:"viewerPath"`
	// ViewerOrigin says which origin ViewerPath is relative to:
	//
	//   "self" — router-manager serves the viewer itself, so it is always
	//     the origin this page is already on. Nothing to configure.
	//   "app"  — ViewerPath is an /app/ path, which router's nginx serves
	//     only on the *shared* hostname; a SPA opened on a dedicated
	//     ROUTER_MANAGER_HOSTS domain has to be told that origin
	//     (ROUTER_APP_ORIGIN) or it can't render a viewer at all.
	//
	// Sent per target rather than derived from Backend on the frontend, so
	// adding a backend doesn't need a matching frontend change - the same
	// reason vncTargetsResponse ships Backends at all.
	ViewerOrigin string `json:"viewerOrigin"`
	// RouteMissing: no /app/<name>.caddy fragment at all (deleted from the
	// App Routes tab, or a failed create left the store ahead of reality).
	// Always false for a backend that doesn't use an App Route.
	RouteMissing bool `json:"routeMissing"`
	// RouteDiverged: a fragment exists but no longer matches this target —
	// hand-edited into a shape approutes can't parse, or repointed
	// elsewhere. The viewer would then show something other than this
	// target, so it's worth surfacing rather than silently trusting.
	RouteDiverged bool `json:"routeDiverged"`
}

// ViewerOrigin values - see Info.ViewerOrigin.
const (
	ViewerOriginSelf = "self"
	ViewerOriginApp  = "app"
)

func viewerOrigin(t Target) string {
	if usesAppRoute(t) {
		return ViewerOriginApp
	}
	return ViewerOriginSelf
}

func validate(t Target) error {
	if err := devproxy.ValidateName(t.Name); err != nil {
		return err
	}
	if _, ok := backends[t.Backend]; !ok {
		return fmt.Errorf("%w: unknown backend %q (known: %s)", ErrValidation, t.Backend, strings.Join(Backends(), ", "))
	}
	// "" is legal: it means "the default", which is what every target
	// stored before this field existed carries.
	if _, ok := resizeModes[t.ResizeMode]; !ok && t.ResizeMode != "" {
		return fmt.Errorf("%w: unknown resize mode %q (known: %s, %s, %s)", ErrValidation, t.ResizeMode, ResizeRemote, ResizeScale, ResizeOff)
	}
	// RequireAuth is an App-Routes-level flag (it renders a tinyauth
	// forward_auth into the app's Caddyfile fragment), so it has nothing to
	// act on for a backend with no App Route. Refused rather than ignored:
	// silently accepting it would leave a target the user believes is
	// behind a login sitting wide open, which is the worst possible way to
	// get this wrong. router-manager's own password gates the BackendRFB
	// socket instead - see backend/main.go's /api/vnc/targets/{name}/ws.
	if t.RequireAuth && !usesAppRoute(t) {
		return fmt.Errorf("%w: the %q backend has no App Route to put tinyauth in front of - it is gated by router-manager's own password instead (설정 tab), so leave 인증 요구 off here", ErrValidation, t.Backend)
	}
	// Label is display-only (never reaches a Caddyfile), so it only needs
	// to be sane to render — no control characters, bounded length.
	if len(t.Label) > 200 {
		return fmt.Errorf("%w: label must be 200 characters or fewer", ErrValidation)
	}
	if strings.ContainsFunc(t.Label, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return fmt.Errorf("%w: label must not contain control characters", ErrValidation)
	}
	// Same allowlist/self-SSRF guard App Routes applies — checked up front
	// so a bad target fails before anything is written, even though
	// approutes.Create would reject it again anyway.
	return approutes.ValidateTarget(t.Target)
}

func viewerPath(t Target) string {
	b, ok := backends[t.Backend]
	if !ok {
		return ""
	}
	return b.viewer(t)
}

// usesAppRoute reports whether t's transport is an App Routes fragment.
// An unknown backend answers false: nothing should be created for it, and
// validate refuses it long before this matters.
func usesAppRoute(t Target) bool {
	return backends[t.Backend].appRoute
}

func load() ([]Target, error) {
	data, err := os.ReadFile(StorePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []Target{}, nil
		}
		return nil, err
	}
	var targets []Target
	if err := json.Unmarshal(data, &targets); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w", StorePath, err)
	}
	if targets == nil {
		targets = []Target{}
	}
	return targets, nil
}

func save(targets []Target) error {
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
	data, err := json.MarshalIndent(targets, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(StorePath, append(data, '\n'), 0o644, 0o755)
}

func indexOf(targets []Target, name string) int {
	for i, t := range targets {
		if t.Name == name {
			return i
		}
	}
	return -1
}

// routeState reports how name's App Route fragment compares to t.
func routeState(routes map[string]approutes.Info, t Target) (missing, diverged bool) {
	if !usesAppRoute(t) {
		return false, false
	}
	info, ok := routes[t.Name]
	if !ok {
		return true, false
	}
	if info.Structured == nil {
		// Hand-edited past what Render produces — can't tell what it
		// points at, so treat it as diverged rather than guessing.
		return false, true
	}
	return false, info.Structured.Target != t.Target || info.Structured.RequireAuth != t.RequireAuth
}

// List returns every registered target, each cross-checked against the live
// App Routes fragments. Returns an empty (never nil) slice so the JSON body
// is always [] rather than null.
func List() ([]Info, error) {
	mu.Lock()
	defer mu.Unlock()

	targets, err := load()
	if err != nil {
		return nil, err
	}
	// A failure to read App Routes shouldn't blank the whole tab — the
	// registry itself is still perfectly readable, so fall back to
	// reporting no drift rather than erroring out.
	routes := map[string]approutes.Info{}
	if list, err := approutes.List(); err == nil {
		for _, info := range list {
			routes[info.Name] = info
		}
	}

	result := make([]Info, 0, len(targets))
	for _, t := range targets {
		missing, diverged := routeState(routes, t)
		result = append(result, Info{
			Target:        t,
			ViewerPath:    viewerPath(t),
			ViewerOrigin:  viewerOrigin(t),
			RouteMissing:  missing,
			RouteDiverged: diverged,
		})
	}
	return result, nil
}

// app is the App Route that carries t.
func app(t Target) approutes.App {
	return approutes.App{Name: t.Name, Target: t.Target, RequireAuth: t.RequireAuth}
}

// Get returns one stored target. The WebSocket bridge
// (backend/handlers_vnc.go) is the caller that matters: it needs the
// target's address on every connection, and must read it from the store
// each time rather than from anything cached, so an edited target takes
// effect on the next connect instead of at the next router restart.
func Get(name string) (Target, error) {
	mu.Lock()
	defer mu.Unlock()

	targets, err := load()
	if err != nil {
		return Target{}, err
	}
	i := indexOf(targets, name)
	if i < 0 {
		return Target{}, ErrTargetNotFound
	}
	return targets[i], nil
}

// Create registers a target and, for a backend that needs one, creates its
// App Route.
//
// The route is written first and the registry second, because the route is
// the part with real validation and a real conflict check (approutes.Create
// returns ErrAppExists if some non-VNC app already owns that path segment,
// which is exactly the hijack this ordering prevents). If the registry
// write then fails, the just-created route is rolled back so a failed
// create doesn't leave a stray app behind.
//
// approutes.ErrReloadFailed is the one error that arrives *after* the
// fragment is already durably written — treat it as success for
// bookkeeping purposes (record the target, so the tab reflects what's on
// disk) and surface the error anyway, same as approutes' own callers do.
func Create(ctx context.Context, t Target) error {
	if err := validate(t); err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	targets, err := load()
	if err != nil {
		return err
	}
	if indexOf(targets, t.Name) >= 0 {
		return ErrTargetExists
	}

	var createErr error
	if usesAppRoute(t) {
		createErr = createRoute(ctx, t)
		if createErr != nil && !errors.Is(createErr, approutes.ErrReloadFailed) {
			return createErr
		}
	}

	targets = append(targets, t)
	if err := save(targets); err != nil {
		// Roll back so the App Routes tab doesn't grow an orphan the VNC
		// tab has no record of. Best-effort: if this also fails there's
		// nothing further to try, and the original error is the useful one.
		if usesAppRoute(t) {
			_ = approutes.Delete(ctx, t.Name)
		}
		return err
	}
	return createErr
}

// createRoute wraps approutes.Create with the one error message that needs
// translating for a caller who never mentioned App Routes.
func createRoute(ctx context.Context, t Target) error {
	err := approutes.Create(ctx, app(t))
	if errors.Is(err, approutes.ErrAppExists) {
		// Refusing here is the point (it's what stops a VNC target from
		// silently repointing someone else's app), but bare "app already
		// exists" reads as a non-sequitur from a tab that never mentioned
		// App Routes. Most likely cause by far: a VNC target wired up by
		// hand through the App Routes tab before this tab existed.
		return fmt.Errorf("%w: an App Route named %q already exists - delete it from the App Routes tab first, then add the target here (this tab creates its own App Route)", err, t.Name)
	}
	return err
}

// Update overwrites oldName's target (renaming it when t.Name differs) and
// brings its App Route along. A target whose route went missing is
// re-created rather than failing — the tab's own self-heal for a fragment
// deleted from the App Routes tab, which is otherwise a dead end the user
// can only fix by deleting and re-adding the target.
//
// Switching Backend between an App-Route-backed one and a router-side one
// is a real edit, not an error: whether a route should exist is decided by
// the *stored* target on the way in and by t on the way out, so the route
// is created, updated or removed to match. Without that, flipping a target
// to BackendRFB would leave its old /app/<name>/ route serving the target's
// web port to anyone who kept the URL.
func Update(ctx context.Context, oldName string, t Target) error {
	if err := validate(t); err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	targets, err := load()
	if err != nil {
		return err
	}
	i := indexOf(targets, oldName)
	if i < 0 {
		return ErrTargetNotFound
	}
	if t.Name != oldName && indexOf(targets, t.Name) >= 0 {
		return ErrTargetExists
	}

	had, wants := usesAppRoute(targets[i]), usesAppRoute(t)
	var updateErr error
	switch {
	case had && wants:
		updateErr = approutes.UpdateStructured(ctx, oldName, app(t))
		if errors.Is(updateErr, approutes.ErrAppNotFound) {
			updateErr = createRoute(ctx, t)
		}
	case had && !wants:
		updateErr = approutes.Delete(ctx, oldName)
		if errors.Is(updateErr, approutes.ErrAppNotFound) {
			updateErr = nil
		}
	case !had && wants:
		updateErr = createRoute(ctx, t)
	}
	if updateErr != nil && !errors.Is(updateErr, approutes.ErrReloadFailed) {
		return updateErr
	}

	targets[i] = t
	if err := save(targets); err != nil {
		return err
	}
	return updateErr
}

// Delete removes a target and, if it had one, its App Route. A route that's
// already gone isn't an error — the registry entry is still worth removing,
// and that's the whole point of the call.
//
// Gated on the *stored* target's backend rather than deleting
// unconditionally: a router-side target never created an App Route, so an
// app that happens to share its name belongs to someone else, and removing
// it here would be destroying a stranger's config on the way past.
func Delete(ctx context.Context, name string) error {
	mu.Lock()
	defer mu.Unlock()

	targets, err := load()
	if err != nil {
		return err
	}
	i := indexOf(targets, name)
	if i < 0 {
		return ErrTargetNotFound
	}
	if !usesAppRoute(targets[i]) {
		targets = append(targets[:i], targets[i+1:]...)
		return save(targets)
	}

	deleteErr := approutes.Delete(ctx, name)
	if deleteErr != nil && !errors.Is(deleteErr, approutes.ErrAppNotFound) && !errors.Is(deleteErr, approutes.ErrReloadFailed) {
		return deleteErr
	}
	if errors.Is(deleteErr, approutes.ErrAppNotFound) {
		deleteErr = nil
	}

	targets = append(targets[:i], targets[i+1:]...)
	if err := save(targets); err != nil {
		return err
	}
	return deleteErr
}
