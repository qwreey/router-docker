import { useMemo } from 'react'
import { useAuthStatus } from '../common/useAuthStatus'
import type { VncTargetInfo } from '../../api/types'

// Which origin a VNC viewer page has to be loaded from — NOT necessarily
// the one this SPA is served from, which is the whole reason this hook
// exists rather than a bare window.location.origin.
//
// It depends on the target's own viewerOrigin (see
// router/backend/internal/vnc.Info):
//
//   'self' — router-manager serves the viewer itself (a BackendRFB target,
//     the default). Always this page's own origin, on every deployment,
//     with nothing to configure. This is one of the reasons that backend
//     exists.
//   'app'  — the viewer is the target's own, reached through an /app/
//     path. router's nginx serves /app/ only on the *shared* hostname; the
//     dedicated ROUTER_MANAGER_HOSTS block deliberately serves nothing but
//     router-manager itself (see config/nginx/nginx-service.default.sh),
//     because putting user-registered, possibly-third-party app content on
//     router-manager's own origin is exactly the ambient-cookie exposure
//     that whole feature exists to close. So on a dedicated domain,
//     `/app/...` doesn't resolve to the viewer at all — it falls through to
//     static.go's SPA-shell fallback and renders this same page inside
//     itself.
//
// For 'app', four cases in priority order:
//  1. ?origin= — set by webmanager's RouterFrame.tsx, which knows its own
//     (shared) origin and passes it into the embed. This is the normal path
//     for the webmanager VNC tab once ROUTER_MANAGER_HOSTS is configured.
//  2. Served from a ROUTER_MANAGER_HOSTS domain, with ROUTER_APP_ORIGIN
//     configured: that value, cross-origin to this page by design.
//  3. Served from a ROUTER_MANAGER_HOSTS domain with neither: null — there
//     is no origin we can honestly derive, so the tab says so (and names
//     the env var) instead of rendering an iframe that would silently show
//     its own shell back.
//  4. Otherwise the shared origin already is the current one.
export function useViewerOrigin(info: VncTargetInfo | null): { origin: string | null; loading: boolean } {
  const { status } = useAuthStatus()

  const paramOrigin = useMemo(() => {
    const raw = new URLSearchParams(window.location.search).get('origin')
    return parseOrigin(raw)
  }, [])

  // No target open: nothing to resolve, and reporting "loading" would leave
  // the tab's own placeholder waiting on an answer it never needs.
  if (!info) return { origin: null, loading: false }
  if (info.viewerOrigin === 'self') return { origin: window.location.origin, loading: false }

  if (paramOrigin) return { origin: paramOrigin, loading: false }
  // status===null only means the auth-status fetch is still in flight; we
  // can't tell the dedicated-domain cases from the shared one until it
  // lands, so report loading rather than guessing the current origin and
  // flashing a viewer that then disappears.
  if (!status) return { origin: null, loading: true }
  if (status.trustedHosts.includes(status.requestHost)) {
    // Re-parsed rather than trusted as-is even though the backend already
    // normalized it (see handlers_auth.go's appOrigin) — this value ends up
    // in an iframe src, and one validation per boundary is cheap.
    return { origin: parseOrigin(status.appOrigin), loading: false }
  }
  return { origin: window.location.origin, loading: false }
}

// Never interpolate an origin that came in over the wire straight into an
// iframe src: parse it and keep only the origin, so a `javascript:` or
// data: URL can't ride in.
function parseOrigin(raw: string | null | undefined): string | null {
  if (!raw) return null
  try {
    const url = new URL(raw)
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return null
    return url.origin
  } catch {
    return null
  }
}
