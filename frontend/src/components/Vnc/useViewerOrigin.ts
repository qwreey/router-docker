import { useMemo } from 'react'
import { useAuthStatus } from '../common/useAuthStatus'

// Which origin the VNC viewer page (/app/<name>/...) has to be loaded from
// — NOT necessarily the one this SPA is served from, which is the whole
// reason this hook exists rather than a bare window.location.origin.
//
// router's nginx serves /app/ only on the *shared* hostname's server block.
// The dedicated ROUTER_MANAGER_HOSTS block deliberately serves nothing but
// router-manager itself (see router/config/nginx/nginx-service.default.sh),
// because putting user-registered, possibly-third-party app content on
// router-manager's own origin is exactly the ambient-cookie exposure that
// whole feature exists to close. So when the SPA is being served from a
// dedicated router-manager domain, `/app/...` on the current origin does
// not resolve to the viewer at all — it falls through to static.go's
// SPA-shell fallback and renders this same page inside itself.
//
// Three cases, in priority order:
//  1. ?origin= — set by webmanager's RouterFrame.tsx, which knows its own
//     (shared) origin and passes it into the embed. This is the normal path
//     for the webmanager VNC tab once ROUTER_MANAGER_HOSTS is configured.
//  2. Served from a ROUTER_MANAGER_HOSTS domain with no ?origin= (someone
//     opened the dedicated domain directly): null — there is no origin we
//     can honestly derive, so the tab says so instead of rendering an
//     iframe that would silently show its own shell back.
//  3. Otherwise the shared origin already is the current one (/router/ on
//     the main hostname, the default when ROUTER_MANAGER_HOSTS is unset).
export function useViewerOrigin(): { origin: string | null; loading: boolean } {
  const { status } = useAuthStatus()

  const paramOrigin = useMemo(() => {
    const raw = new URLSearchParams(window.location.search).get('origin')
    if (!raw) return null
    // Never interpolate an untrusted query param straight into an iframe
    // src: parse it and keep only the origin, so a `javascript:` or
    // data: URL can't ride in through this parameter.
    try {
      const url = new URL(raw)
      if (url.protocol !== 'http:' && url.protocol !== 'https:') return null
      return url.origin
    } catch {
      return null
    }
  }, [])

  if (paramOrigin) return { origin: paramOrigin, loading: false }
  // status===null only means the auth-status fetch is still in flight; we
  // can't tell case 2 from case 3 until it lands, so report loading rather
  // than guessing the current origin and flashing a viewer that then
  // disappears.
  if (!status) return { origin: null, loading: true }
  if (status.trustedHosts.includes(status.requestHost)) return { origin: null, loading: false }
  return { origin: window.location.origin, loading: false }
}
