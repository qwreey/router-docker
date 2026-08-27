package devproxy

import (
	"fmt"
	"testing"
)

// Dev Proxy's own half of approutes' TestRenderParseRoundTrip - same
// template change, one indent level deeper, and its own hand-written
// line-by-line parser.
func TestRenderParseRoundTrip(t *testing.T) {
	for _, requireAuth := range []bool{false, true} {
		t.Run(fmt.Sprintf("requireAuth=%v", requireAuth), func(t *testing.T) {
			want := Expose{
				Name: "dev",
				Host: "dev.example.com",
				Routes: []Route{{
					Mode:        "handle",
					Target:      "code-docker:3000",
					RequireAuth: requireAuth,
				}},
			}
			got, ok := parseStructured(want.Name, Render(want))
			if !ok {
				t.Fatalf("parseStructured refused its own Render output:\n%s", Render(want))
			}
			if len(got.Routes) != 1 || got.Routes[0] != want.Routes[0] || got.Host != want.Host {
				t.Fatalf("round trip changed the expose: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestParseLegacyForwardAuth(t *testing.T) {
	legacy := "@dev host dev.example.com\n" +
		"handle @dev {\n" +
		"\thandle {\n" +
		"\t\tforward_auth " + TinyauthTarget + " {\n" +
		"\t\t\turi " + TinyauthVerifyURI + "\n" +
		"\t\t}\n" +
		"\t\treverse_proxy code-docker:3000\n" +
		"\t}\n" +
		"}\n"

	got, ok := parseStructured("dev", legacy)
	if !ok {
		t.Fatalf("parseStructured refused a legacy fragment:\n%s", legacy)
	}
	if len(got.Routes) != 1 || !got.Routes[0].RequireAuth {
		t.Fatalf("legacy parse lost requireAuth: %+v", got)
	}
	if Render(got) == legacy {
		t.Fatal("Render reproduced the legacy block - Normalize would never repair anything")
	}
}
