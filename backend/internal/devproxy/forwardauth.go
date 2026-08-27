package devproxy

import "strings"

// The tinyauth forward_auth block that a Dev Proxy route or an App Route
// with RequireAuth gets. Lives here (rather than being spelled out inline
// in each package's own Render) because internal/approutes emits the exact
// same block one indent level shallower, and every line of it turned out to
// be load-bearing in a way a second hand-maintained copy would quietly get
// wrong - see each line's own note below.
//
// What was here before this file was just:
//
//	forward_auth 127.0.0.1:3000 {
//		uri /api/auth/caddy
//	}
//
// which is the block tinyauth's own Caddy docs start from, and it never
// worked at all in router's topology. Both reasons were confirmed live
// against tinyauth v5.1.3 + Caddy v2.11.4 (2026-08-27):
//
//  1. Caddy sets NO X-Forwarded-Proto and NO X-Forwarded-Host on a request
//     it received over a **unix-socket listener** - not even passing
//     through ones the incoming request already carried. Every Caddy
//     listener router actually serves through is a unix socket (nginx ->
//     /run/caddy-adapter.sock for /exports/, /run/caddy-app.sock for
//     /app/, see caddy-adapter.default.sh), and tinyauth answers **400 Bad
//     Request** when either header is missing. So every auth-required
//     request 400'd before it ever got as far as "are you logged in" -
//     which is why "인증 요구" appeared to break the target outright
//     rather than prompting for a login.
//  2. tinyauth answers an *unauthenticated* request with 401 + an
//     X-Tinyauth-Location header, NOT a 302. Caddy's forward_auth copies a
//     non-2xx response through verbatim unless a handle_response says
//     otherwise, so the browser got a bare `{"message":"Unauthorized"}`
//     JSON body and no login page ever appeared.
const tinyauthLocationHeader = "X-Tinyauth-Location"

// legacyForwardAuthBlock is the pre-fix three-line block above. Still
// recognized when parsing so an existing fragment written by an older
// router doesn't lose structured-form editing (and doesn't show up as
// "drift" in the VNC tab) - but never written again: Normalize rewrites
// any fragment still carrying it, see normalize.go.
func legacyForwardAuthBlock(indent string) []string {
	return []string{
		indent + "forward_auth " + TinyauthTarget + " {",
		indent + "\turi " + TinyauthVerifyURI,
		indent + "}",
	}
}

// ForwardAuthBlock returns the current block's lines, each already prefixed
// with indent (the leading tabs of the block's own outermost line).
func ForwardAuthBlock(indent string) []string {
	return []string{
		indent + "forward_auth " + TinyauthTarget + " {",
		indent + "\turi " + TinyauthVerifyURI,
		// The *original* request URI, not the one Caddy is about to
		// proxy: App Routes' own handle_path has already stripped
		// /app/<name> by the time forward_auth runs, and tinyauth builds
		// its post-login redirect_uri out of this header - so without
		// this a successful login lands the browser on /vnc.html instead
		// of /app/vnc/vnc.html. Harmless for Dev Proxy routes that do no
		// stripping (orig_uri == uri there).
		indent + "\theader_up X-Forwarded-Uri {http.request.orig_uri}",
		// Both of these are the unix-socket-listener workaround from (1)
		// above. `caddy adapt` emits an "Unnecessary header_up
		// X-Forwarded-Host" lint warning for the second one - that
		// heuristic is simply wrong here (it assumes the default
		// pass-through behavior, which a unix listener doesn't do); the
		// warning goes to stderr on an otherwise-successful adapt and is
		// ignored by writeAndValidate.
		indent + "\theader_up X-Forwarded-Host {http.request.host}",
		// Deliberately the *incoming* header rather than {scheme}: router
		// terminates plain HTTP behind whatever outer reverse proxy does
		// TLS, so {scheme} is always "http" here and tinyauth would send
		// the browser back to an http:// URL after login - which a
		// viewer embedded in an https page can't even follow (blocked as
		// mixed content). router's own nginx guarantees this header is
		// always present and correct, defaulting it to its own $scheme
		// when the outer proxy didn't send one (see
		// config/nginx/nginx.default.conf's $router_forwarded_proto map).
		indent + "\theader_up X-Forwarded-Proto {http.request.header.X-Forwarded-Proto}",
		// Matched on the header's presence as well as the status, so a
		// tinyauth *error* (the 400 above, a misconfiguration, ...) still
		// surfaces as itself instead of turning into a 302 with an empty
		// Location - which browsers resolve to the current URL and would
		// spin as a redirect loop.
		indent + "\t@tinyauth_login {",
		indent + "\t\tstatus 4xx",
		indent + "\t\theader " + tinyauthLocationHeader + " *",
		indent + "\t}",
		indent + "\thandle_response @tinyauth_login {",
		indent + "\t\tredir * {rp.header." + tinyauthLocationHeader + "}",
		indent + "\t}",
		indent + "}",
	}
}

// RenderForwardAuth returns ForwardAuthBlock as newline-terminated text,
// ready to append to a fragment being rendered.
func RenderForwardAuth(indent string) string {
	return strings.Join(ForwardAuthBlock(indent), "\n") + "\n"
}

// MatchForwardAuth reports whether body[i:] begins with a tinyauth
// forward_auth block at the given indent, returning the index just past it.
// Accepts the legacy block too - see legacyForwardAuthBlock.
func MatchForwardAuth(body []string, i int, indent string) (next int, ok bool) {
	for _, block := range [][]string{ForwardAuthBlock(indent), legacyForwardAuthBlock(indent)} {
		if matchLines(body, i, block) {
			return i + len(block), true
		}
	}
	return i, false
}

func matchLines(body []string, i int, want []string) bool {
	if i+len(want) > len(body) {
		return false
	}
	for j, line := range want {
		if body[i+j] != line {
			return false
		}
	}
	return true
}
