package vnc

import (
	"strings"
	"testing"

	"router/internal/approutes"
)

func TestViewerPathNoVNC(t *testing.T) {
	// No ResizeMode set is the shape of every target stored before the
	// field existed - it must come out as the default, not as an empty
	// resize= that noVNC would reject.
	got := viewerPath(Target{Name: "studio-vnc", Backend: BackendNoVNC})
	want := "/app/studio-vnc/vnc.html?autoconnect=1&reconnect=1&resize=remote"
	if got != want {
		t.Fatalf("viewerPath() = %q, want %q", got, want)
	}
}

func TestViewerPathResizeMode(t *testing.T) {
	for _, mode := range []string{ResizeRemote, ResizeScale, ResizeOff} {
		got := viewerPath(Target{Name: "studio-vnc", Backend: BackendNoVNC, ResizeMode: mode})
		want := "/app/studio-vnc/vnc.html?autoconnect=1&reconnect=1&resize=" + mode
		if got != want {
			t.Fatalf("viewerPath(%q) = %q, want %q", mode, got, want)
		}
	}
}

func TestViewerPathUnknownBackend(t *testing.T) {
	if got := viewerPath(Target{Name: "x", Backend: "selkies"}); got != "" {
		t.Fatalf("viewerPath() = %q, want empty for an unimplemented backend", got)
	}
}

func TestValidateRejectsUnknownBackend(t *testing.T) {
	err := validate(Target{Name: "studio-vnc", Target: "vnc-only:6080", Backend: "selkies"})
	if err == nil || !strings.Contains(err.Error(), "unknown backend") {
		t.Fatalf("validate() = %v, want an unknown-backend error", err)
	}
}

func TestValidateRejectsSelfTarget(t *testing.T) {
	// targetguard.SelfHosts has no opt-out, so this must fail regardless of
	// any allowlist env var - the one guarantee worth pinning here, since
	// this package reaches it indirectly via approutes.ValidateTarget.
	err := validate(Target{Name: "loop", Target: "router:6080", Backend: BackendNoVNC})
	if err == nil || !strings.Contains(err.Error(), "point back at router") {
		t.Fatalf("validate() = %v, want a self-target rejection", err)
	}
}

func TestValidateResizeMode(t *testing.T) {
	// code-docker rather than the vnc-only alias the other cases use:
	// this test needs validate to reach the resize check and *pass*, and
	// targetguard's default allowlist would reject an unrecognized host
	// first (see approutes.ValidateTarget).
	base := Target{Name: "studio-vnc", Target: "code-docker:6080", Backend: BackendNoVNC}

	for _, mode := range []string{"", ResizeRemote, ResizeScale, ResizeOff} {
		target := base
		target.ResizeMode = mode
		if err := validate(target); err != nil {
			t.Fatalf("validate(resizeMode=%q) = %v, want nil", mode, err)
		}
	}

	target := base
	target.ResizeMode = "fit"
	err := validate(target)
	if err == nil || !strings.Contains(err.Error(), "unknown resize mode") {
		t.Fatalf("validate() = %v, want an unknown-resize-mode error", err)
	}
}

func TestValidateRejectsControlCharsInLabel(t *testing.T) {
	err := validate(Target{Name: "studio-vnc", Target: "vnc-only:6080", Backend: BackendNoVNC, Label: "a\nb"})
	if err == nil || !strings.Contains(err.Error(), "control characters") {
		t.Fatalf("validate() = %v, want a control-character rejection", err)
	}
}

func routeMap(name string, structured *approutes.App) map[string]approutes.Info {
	return map[string]approutes.Info{name: {Name: name, Raw: "(unused)", Structured: structured}}
}

func TestRouteStateDetectsDrift(t *testing.T) {
	target := Target{Name: "studio-vnc", Target: "vnc-only:6080", Backend: BackendNoVNC}

	if missing, diverged := routeState(nil, target); !missing || diverged {
		t.Fatalf("routeState(no routes) = (%v, %v), want (true, false)", missing, diverged)
	}

	matching := routeMap(target.Name, &approutes.App{Name: target.Name, Target: "vnc-only:6080"})
	if missing, diverged := routeState(matching, target); missing || diverged {
		t.Fatalf("routeState(matching) = (%v, %v), want (false, false)", missing, diverged)
	}

	repointed := routeMap(target.Name, &approutes.App{Name: target.Name, Target: "somewhere-else:6080"})
	if missing, diverged := routeState(repointed, target); missing || !diverged {
		t.Fatalf("routeState(repointed) = (%v, %v), want (false, true)", missing, diverged)
	}

	// requireAuth flipped behind the tab's back is drift too: the target
	// would then be gated (or ungated) differently than the registry says.
	regated := routeMap(target.Name, &approutes.App{Name: target.Name, Target: "vnc-only:6080", RequireAuth: true})
	if missing, diverged := routeState(regated, target); missing || !diverged {
		t.Fatalf("routeState(regated) = (%v, %v), want (false, true)", missing, diverged)
	}

	// A fragment that no longer round-trips through approutes.Render (hand
	// edited via the raw editor) can't be compared at all - diverged.
	rawOnly := routeMap(target.Name, nil)
	if missing, diverged := routeState(rawOnly, target); missing || !diverged {
		t.Fatalf("routeState(raw-only) = (%v, %v), want (false, true)", missing, diverged)
	}
}

func TestViewerPathRFB(t *testing.T) {
	// The two parameters rfbViewerPath's own comment calls load-bearing:
	// host= pinned empty (so noVNC resolves its socket URL relative to the
	// page rather than from stored settings), and a "../"-relative path (so
	// the browser derives the same /router prefix it was served under,
	// which nginx strips before router-manager ever sees it).
	got := viewerPath(Target{Name: "studio-vnc", Backend: BackendRFB})
	want := "/router/novnc/vnc.html?autoconnect=1&host=&path=..%2Fapi%2Fvnc%2Ftargets%2Fstudio-vnc%2Fws&reconnect=1&resize=remote"
	if got != want {
		t.Fatalf("viewerPath() = %q, want %q", got, want)
	}
}

func TestViewerOriginByBackend(t *testing.T) {
	// The whole point of BackendRFB for the dedicated-domain case: its
	// viewer is served by router-manager, so it never needs an origin the
	// SPA has to be told about.
	if got := viewerOrigin(Target{Backend: BackendRFB}); got != ViewerOriginSelf {
		t.Fatalf("viewerOrigin(rfb) = %q, want %q", got, ViewerOriginSelf)
	}
	if got := viewerOrigin(Target{Backend: BackendNoVNC}); got != ViewerOriginApp {
		t.Fatalf("viewerOrigin(novnc) = %q, want %q", got, ViewerOriginApp)
	}
}

func TestRouteStateIgnoresRouterSideBackends(t *testing.T) {
	// A BackendRFB target never creates an App Route, so the absence of one
	// is the correct state - reporting it as drift would put a permanent
	// "App Route 없음" warning on every such target.
	target := Target{Name: "studio-vnc", Target: "vnc-only:5900", Backend: BackendRFB}
	if missing, diverged := routeState(nil, target); missing || diverged {
		t.Fatalf("routeState(rfb, no routes) = (%v, %v), want (false, false)", missing, diverged)
	}
}

func TestValidateRejectsRequireAuthOnRouterSideBackend(t *testing.T) {
	// Refused rather than ignored: silently accepting it would leave a
	// target the user believes is behind a login wide open.
	err := validate(Target{Name: "studio-vnc", Target: "code-docker:5900", Backend: BackendRFB, RequireAuth: true})
	if err == nil || !strings.Contains(err.Error(), "인증 요구") {
		t.Fatalf("validate() = %v, want a requireAuth rejection", err)
	}
	// ...and the same target without it is fine.
	if err := validate(Target{Name: "studio-vnc", Target: "code-docker:5900", Backend: BackendRFB}); err != nil {
		t.Fatalf("validate() = %v, want nil", err)
	}
}

func TestBackendsOrderPutsRFBFirst(t *testing.T) {
	// The frontend's picker defaults to backends[0], so this order is the
	// default-backend decision, not cosmetics.
	got := Backends()
	if len(got) == 0 || got[0] != BackendRFB {
		t.Fatalf("Backends() = %v, want %q first", got, BackendRFB)
	}
}
