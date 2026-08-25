// Package vnc manages the VNC tab's own target registry — the "웹 대시보드에
// 라이브 임베드" half of .claude/backlog/router-vnc-tab-plan.md's 경로 B,
// whose transport half (noVNC+websockify in front of an unchanged wayvnc,
// plus targetguard.ExtraAllowedHostsEnv so router is allowed to reach a
// sibling project's VNC container at all) already shipped.
//
// A VNC target is NOT a new proxy mechanism: router's Caddy is stock
// (HTTP/WS only, no layer4 plugin), so raw RFB can never ride App
// Routes/Dev Proxy — what actually gets proxied is the target's *web* VNC
// front end (websockify's HTTP+WebSocket port), and App Routes is already
// exactly the right carrier for that. So this package owns only the two
// things App Routes has no concept of, and delegates everything else:
//
//   - the small amount of per-target metadata a viewer needs but a reverse
//     proxy doesn't (a human Label, and which Backend's viewer URL shape to
//     build — see viewerPath), persisted here in StorePath;
//   - keeping a matching approutes fragment in lockstep with it, so
//     registering a target is one action instead of "add an App Route, then
//     remember its name in the VNC tab".
//
// Backend exists from day one with exactly one value because the plan's own
// 결정 사항 says so: "향후 방향은 noVNC와 Selkies를 나란히(타겟별 선택
// 가능하게) 지원하는 것 — 지금은 noVNC 하나만 구현하고, Selkies는 나중에
// 두 번째 백엔드 옵션으로 추가". Adding Selkies later should be a new
// backendViewer entry plus its own sibling-side program, not a schema change
// here.
//
// Deliberately NOT a second source of truth for the proxying itself: List
// re-reads approutes every call and reports drift (RouteMissing/
// RouteDiverged) rather than caching what it wrote, so an app fragment
// deleted or hand-edited from the App Routes tab shows up as a warning in
// the VNC tab instead of a viewer that silently 404s.
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

// BackendNoVNC is the only backend implemented today — noVNC's own
// vnc.html served by websockify in front of an unchanged wayvnc. See the
// package doc comment on why the field exists anyway.
const BackendNoVNC = "novnc"

// backendViewer maps a Backend to the path (relative to the app's own
// /app/<name>/ prefix) that serves its viewer page. A future Selkies entry
// is one more line here.
//
// The query string is deliberately part of this value rather than assembled
// per-request: what's left in it is a property of the viewer
// implementation, not of the target. autoconnect makes the embed connect
// without a click (the whole point of a dashboard tab) and reconnect
// survives a target container restart without the user re-opening the tab.
// The one setting that turned out NOT to be viewer-wide is resize — see
// Target.ResizeMode, which viewerPath appends to this. Notably absent:
// noVNC's `path` setting — websockify accepts the WebSocket upgrade on any
// path and noVNC resolves its default (`websockify`) relative to the page's
// own location, which already lands inside /app/<name>/ and back through
// Caddy's handle_path to the same target. That was confirmed live in the
// plan's own e2e run ("WebSocket 업그레이드+RFB 배너 수신"), so pinning an
// absolute path here would only break it.
var backendViewer = map[string]string{
	BackendNoVNC: "vnc.html?autoconnect=1&reconnect=1",
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

// Backends lists every implemented backend, for the tab's own picker.
func Backends() []string {
	names := make([]string, 0, len(backendViewer))
	for b := range backendViewer {
		names = append(names, b)
	}
	sort.Strings(names)
	return names
}

// Target is one registered VNC target. Name doubles as the App Route name
// and therefore the /app/<name>/ path segment, so it carries
// devproxy.ValidateName's RFC1123-label constraint; Label is display-only.
// Target is the target's *web* VNC front end (websockify's port, e.g.
// "vnc-only:6080"), never its raw RFB port — see the package doc comment.
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
	// components/Vnc/Vnc.tsx's own note on ROUTER_MANAGER_HOSTS.
	ViewerPath string `json:"viewerPath"`
	// RouteMissing: no /app/<name>.caddy fragment at all (deleted from the
	// App Routes tab, or a failed create left the store ahead of reality).
	RouteMissing bool `json:"routeMissing"`
	// RouteDiverged: a fragment exists but no longer matches this target —
	// hand-edited into a shape approutes can't parse, or repointed
	// elsewhere. The viewer would then show something other than this
	// target, so it's worth surfacing rather than silently trusting.
	RouteDiverged bool `json:"routeDiverged"`
}

func validate(t Target) error {
	if err := devproxy.ValidateName(t.Name); err != nil {
		return err
	}
	if _, ok := backendViewer[t.Backend]; !ok {
		return fmt.Errorf("%w: unknown backend %q (known: %s)", ErrValidation, t.Backend, strings.Join(Backends(), ", "))
	}
	// "" is legal: it means "the default", which is what every target
	// stored before this field existed carries.
	if _, ok := resizeModes[t.ResizeMode]; !ok && t.ResizeMode != "" {
		return fmt.Errorf("%w: unknown resize mode %q (known: %s, %s, %s)", ErrValidation, t.ResizeMode, ResizeRemote, ResizeScale, ResizeOff)
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
	suffix, ok := backendViewer[t.Backend]
	if !ok {
		return ""
	}
	return "/app/" + url.PathEscape(t.Name) + "/" + suffix + "&resize=" + resizeMode(t)
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

// Create registers a target and creates its App Route.
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

	createErr := approutes.Create(ctx, app(t))
	if errors.Is(createErr, approutes.ErrAppExists) {
		// Refusing here is the point (it's what stops a VNC target from
		// silently repointing someone else's app), but bare "app already
		// exists" reads as a non-sequitur from a tab that never mentioned
		// App Routes. Most likely cause by far: a VNC target wired up by
		// hand through the App Routes tab before this tab existed.
		return fmt.Errorf("%w: an App Route named %q already exists - delete it from the App Routes tab first, then add the target here (this tab creates its own App Route)", createErr, t.Name)
	}
	if createErr != nil && !errors.Is(createErr, approutes.ErrReloadFailed) {
		return createErr
	}

	targets = append(targets, t)
	if err := save(targets); err != nil {
		// Roll back so the App Routes tab doesn't grow an orphan the VNC
		// tab has no record of. Best-effort: if this also fails there's
		// nothing further to try, and the original error is the useful one.
		_ = approutes.Delete(ctx, t.Name)
		return err
	}
	return createErr
}

// Update overwrites oldName's target (renaming it when t.Name differs) and
// brings its App Route along. A target whose route went missing is
// re-created rather than failing — the tab's own self-heal for a fragment
// deleted from the App Routes tab, which is otherwise a dead end the user
// can only fix by deleting and re-adding the target.
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

	updateErr := approutes.UpdateStructured(ctx, oldName, app(t))
	if errors.Is(updateErr, approutes.ErrAppNotFound) {
		updateErr = approutes.Create(ctx, app(t))
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

// Delete removes a target and its App Route. A route that's already gone
// isn't an error — the registry entry is still worth removing, and that's
// the whole point of the call.
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
