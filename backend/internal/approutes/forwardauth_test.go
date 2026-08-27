package approutes

import (
	"fmt"
	"strings"
	"testing"

	"router/internal/devproxy"
)

// Render -> parseStructured has to round-trip for both RequireAuth values,
// or the App Routes/VNC tabs silently lose structured editing (and the VNC
// tab reports the route as "diverged") the moment the fragment template
// changes. This is the regression test for the 2026-08-27 tinyauth fix,
// which changed the forward_auth block from three lines to thirteen.
func TestRenderParseRoundTrip(t *testing.T) {
	for _, requireAuth := range []bool{false, true} {
		t.Run(fmt.Sprintf("requireAuth=%v", requireAuth), func(t *testing.T) {
			want := App{Name: "vnc", Target: "vnc-only:6080", RequireAuth: requireAuth}
			got, ok := parseStructured(want.Name, Render(want))
			if !ok {
				t.Fatalf("parseStructured refused its own Render output:\n%s", Render(want))
			}
			if got != want {
				t.Fatalf("round trip changed the app: got %+v, want %+v", got, want)
			}
		})
	}
}

// A fragment written by a router older than the tinyauth fix must still
// parse - otherwise every existing auth-required app would show up as
// hand-edited raw text and Normalize (which only touches fragments that
// parse) could never repair it, which is the entire point of that step.
func TestParseLegacyForwardAuth(t *testing.T) {
	legacy := "handle_path /app/vnc/* {\n" +
		"\tforward_auth " + devproxy.TinyauthTarget + " {\n" +
		"\t\turi " + devproxy.TinyauthVerifyURI + "\n" +
		"\t}\n" +
		"\treverse_proxy vnc-only:6080 {\n" +
		fmt.Sprintf("\t\theader_down Location %q %q\n", locationRewriteSearch, locationRewriteReplace("vnc")) +
		"\t}\n" +
		"}\n"

	got, ok := parseStructured("vnc", legacy)
	if !ok {
		t.Fatalf("parseStructured refused a legacy fragment:\n%s", legacy)
	}
	want := App{Name: "vnc", Target: "vnc-only:6080", RequireAuth: true}
	if got != want {
		t.Fatalf("legacy parse: got %+v, want %+v", got, want)
	}
	// ...and re-rendering it must produce the fixed block, since that's what
	// Normalize writes back.
	rendered := Render(got)
	if rendered == legacy {
		t.Fatal("Render reproduced the legacy block - Normalize would never repair anything")
	}
	if !strings.Contains(rendered, "handle_response @tinyauth_login") {
		t.Fatalf("re-rendered fragment has no login redirect:\n%s", rendered)
	}
}

// A forward_auth shape this build doesn't recognize must fail the
// structured parse rather than being dropped: Render would otherwise write
// the fragment back out without it, silently removing the auth.
func TestUnknownForwardAuthRefusesStructuredParse(t *testing.T) {
	raw := "handle_path /app/vnc/* {\n" +
		"\tforward_auth 127.0.0.1:9999 {\n\t\turi /whatever\n\t}\n" +
		"\treverse_proxy vnc-only:6080 {\n" +
		fmt.Sprintf("\t\theader_down Location %q %q\n", locationRewriteSearch, locationRewriteReplace("vnc")) +
		"\t}\n" +
		"}\n"
	if _, ok := parseStructured("vnc", raw); ok {
		t.Fatal("parseStructured accepted an unrecognized forward_auth block")
	}
}
