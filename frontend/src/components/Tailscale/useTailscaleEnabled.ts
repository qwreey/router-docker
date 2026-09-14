import { useEffect, useState } from 'react'
import { api } from './client'

const POLL_INTERVAL_MS = 5000

// null = still loading (SidebarContainer just shows the tab until this
// resolves, same as it always has, rather than flashing a false "removed"
// state). A fetch failure also resolves to true - degrading to "enabled"
// (the old, always-shown behavior) is safer than hiding the tab over a
// transient error.
//
// Re-polls every POLL_INTERVAL_MS instead of fetching once on mount - see
// the near-identical webmanager copy of this hook
// (webmanager/frontend/src/components/RouterEmbed/useTailscaleEnabled.ts)
// for why a one-shot fetch made the tab feel very slow to disappear after
// TAILSCALE_ENABLED was flipped off.
//
// Reads /state, not /status, even though both carry `enabled`. /status is
// behind the password gate as of the 2026-09-07 security review (it returns
// the whole tailnet peer list - hostnames, IPs, tags, online state - which
// is not a "low-risk read"), and a *background 5s poll* of a gated route is
// exactly the wrong thing to own: api/client.ts turns any 401 into an unlock
// prompt, so a locked SPA would pop the password modal on its own every time
// this ticked, with nothing the user did to cause it. /state stays open
// (code-server's own sign-in banner polls it), carries the same `enabled`
// bool, and reveals only backendState/authUrl.
export function useTailscaleEnabled(): boolean | null {
  const [enabled, setEnabled] = useState<boolean | null>(null)

  useEffect(() => {
    let cancelled = false

    const check = () => {
      api
        .get<{ enabled: boolean }>('/state')
        .then((res) => {
          if (!cancelled) setEnabled(res.enabled)
        })
        .catch(() => {
          if (!cancelled) setEnabled(true)
        })
    }

    check()
    const timer = setInterval(check, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [])

  return enabled
}
