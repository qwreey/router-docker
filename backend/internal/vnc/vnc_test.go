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
