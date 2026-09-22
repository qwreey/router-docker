import { Fragment, useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { ExternalLink } from 'lucide-react'
import { vncApi as api, errorMessage, requestUnlock } from '../../api/client'
import type { VncTarget, VncTargetInfo, VncTargetsResponse } from '../../api/types'
import { ErrorBanner } from '../common/ErrorBanner'
import { Skeleton } from '../common/Skeleton'
import { ConfirmDialog } from '../common/ConfirmDialog'
import { withViewTransition } from '../../utils/viewTransition'
import { TargetDialog } from './TargetDialog'
import { useViewerOriginResolver } from './useViewerOrigin'
import { useAuthStatus } from '../common/useAuthStatus'
import { useVncClients } from './useVncClients'
import { VncClientsBadge, VncClientsPanel } from './VncClientsPanel'
import { HOME_TAB, VncTabs, tabLabel } from './VncTabs'
import { notifyEmbedParent } from '../../embedTheme'
import './Vnc.css'

const BACKEND_LABEL: Record<string, string> = {
  rfb: 'RFB (router 중계)',
  novnc: '대상의 웹 VNC',
}

// '' is a target stored before resizeMode existed; the backend resolves it
// to remote, so the list says the same thing the viewer will actually do.
const RESIZE_LABEL: Record<string, string> = {
  '': '원격 해상도',
  remote: '원격 해상도',
  scale: '맞춰 축소',
  off: '안 함',
}

// How long to let the handed-over embed's connection finish tearing down
// before the new window connects in its place. Only the WebSocket close has
// to land at router, which then drops the upstream TCP - well under this in
// practice, but a blank window for a fraction of a second is a much cheaper
// failure than two clients briefly sharing a target.
const HANDOFF_DELAY_MS = 250

// Which targets are open as tabs, in tab order, and which tab is showing.
// Per-browser UI state, so localStorage (this origin's - standalone and a
// dedicated ROUTER_MANAGER_HOSTS domain each keep their own). Reopening
// after a reload reconnects; there's no server-side session to resume.
const TABS_KEY = 'router.vnc.tabs'

interface TabState {
  open: string[]
  active: string
}

// ?mode=host: something outside this page owns the tabs - the code-server
// extension, where each target is its own editor tab (webmanager passes this
// on when it is embedded there). The page then shows exactly one thing: the
// ?target= viewer, or with no target the list, whose "열기" asks the parent
// to open that target rather than opening it here. Nothing is persisted:
// each editor tab restores itself from its own URL, and a shared stored set
// would make every view reopen every target.
const HOST_MODE = new URLSearchParams(window.location.search).get('mode') === 'host'

function loadTabs(): TabState {
  if (HOST_MODE) {
    const target = new URLSearchParams(window.location.search).get('target')
    return target ? { open: [target], active: target } : { open: [], active: HOME_TAB }
  }
  let state: TabState = { open: [], active: HOME_TAB }
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(TABS_KEY) || 'null')
    if (parsed && typeof parsed === 'object') {
      const { open, active } = parsed as Partial<TabState>
      const names = Array.isArray(open) ? open.filter((v): v is string => typeof v === 'string') : []
      state = { open: names, active: typeof active === 'string' && names.includes(active) ? active : HOME_TAB }
    }
  } catch {
    // unavailable or garbage - start from the list
  }
  // ?target=<name> (a deep link, e.g. from webmanager) opens and shows that
  // target on top of whatever was restored.
  const wanted = new URLSearchParams(window.location.search).get('target')
  if (wanted) state = { open: state.open.includes(wanted) ? state.open : [...state.open, wanted], active: wanted }
  return state
}

function saveTabs(state: TabState) {
  if (HOST_MODE) return
  try {
    localStorage.setItem(TABS_KEY, JSON.stringify(state))
  } catch {
    // still works for this page's lifetime, just won't survive a reload
  }
}

// The viewer iframe's own document, or null when it can't be reached:
// cross-origin (the webmanager embed against a dedicated
// ROUTER_MANAGER_HOSTS domain - see useViewerOrigin) or simply not loaded
// yet.
function novncDocument(frame: HTMLIFrameElement | null | undefined): Document | null {
  if (!frame) return null
  try {
    return frame.contentDocument
  } catch {
    return null
  }
}

// Is anything of ours fullscreen right now? Which document owns it depends
// on which path handleFullscreen took, so both are checked.
function isFullscreenActive(frame: HTMLIFrameElement | null | undefined): boolean {
  return Boolean(document.fullscreenElement || novncDocument(frame)?.fullscreenElement)
}

function PaneNotice({ children }: { children: ReactNode }) {
  return (
    <div className="vnc-pane-notice">
      <div className="card">
        <div className="info-note">
          <span aria-hidden="true">⚠</span>
          {children}
        </div>
      </div>
    </div>
  )
}

export function Vnc() {
  const [targets, setTargets] = useState<VncTargetInfo[]>([])
  const [backends, setBackends] = useState<string[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [dialog, setDialog] = useState<{ target: VncTarget | null } | null>(null)
  const [dialogError, setDialogError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [tabs, setTabsState] = useState<TabState>(loadTabs)
  // Which target's "연결된 클라이언트" row is expanded, if any.
  const [expandedClients, setExpandedClients] = useState<string | null>(null)
  const { clients: vncClients, refresh: refreshVncClients } = useVncClients(targets.map((t) => t.name))

  const resolveViewerOrigin = useViewerOriginResolver()
  // A window opened by openInNewWindow that is deliberately still sitting on
  // about:blank, waiting for the embedded viewer it is taking over from to
  // be gone. See that function.
  const pendingWindow = useRef<{ name: string; win: Window; url: string } | null>(null)
  const sectionRef = useRef<HTMLElement>(null)
  const frames = useRef(new Map<string, HTMLIFrameElement>())
  const [fullscreen, setFullscreen] = useState(false)
  // Bumped on every viewer load, so the fullscreen listener below can
  // re-attach to the newly reachable inner document.
  const [frameLoads, setFrameLoads] = useState(0)
  // A BackendRFB viewer's WebSocket goes through router-manager's own
  // password gate, so a locked session gets a viewer that loads and then
  // silently fails to connect. Say so instead.
  const { status: authStatus, refresh: refreshAuthStatus } = useAuthStatus()

  const setTabs = useCallback((update: (prev: TabState) => TabState) => {
    setTabsState((prev) => {
      const next = update(prev)
      saveTabs(next)
      return next
    })
  }, [])

  const openTab = (name: string) =>
    HOST_MODE
      ? notifyEmbedParent('vnc-open-request', { name })
      : setTabs((prev) => ({ open: prev.open.includes(name) ? prev.open : [...prev.open, name], active: name }))

  // Closing unmounts the viewer's iframe, which is what closes its
  // WebSocket - there is no "keep it running" for a live screen. The tab to
  // the left takes over, or the list when it was the first one.
  const closeTab = (name: string) =>
    setTabs((prev) => {
      const index = prev.open.indexOf(name)
      if (index === -1) return prev
      const open = prev.open.filter((n) => n !== name)
      const active = prev.active !== name ? prev.active : (open[index - 1] ?? open[index] ?? HOME_TAB)
      return { open, active }
    })

  // The embedding parent (webmanager's RouterFrame) is told which tabs are
  // open and which is showing, so it can put the active one in its own URL.
  useEffect(() => {
    notifyEmbedParent('vnc-state', { open: tabs.open, active: tabs.active === HOME_TAB ? null : tabs.active })
  }, [tabs])

  // Unlock from *this* page, not "go find the sidebar": when this page is
  // the <iframe> inside webmanager's VNC tab, the only sidebar the user can
  // see is webmanager's own, whose lock button unlocks webmanager's gate -
  // a different process with a different cookie - so pointing at it sent
  // them to the wrong lock. requestUnlock() shares RouterUnlockModalHost with
  // the sidebar footer and the 401 path, so it works identically embedded
  // or standalone.
  async function handleUnlockClick() {
    try {
      await requestUnlock()
      await refreshAuthStatus()
    } catch {
      // user cancelled the prompt - nothing to do
    }
  }

  // Second half of the handoff: the embed has now been unmounted (effects
  // run after the DOM commit), so its WebSocket is closing and router is
  // dropping the upstream TCP connection with it. The short delay is for
  // that teardown to actually reach the VNC server - reconnecting while the
  // old client is still registered is the very thing this avoids.
  useEffect(() => {
    const pending = pendingWindow.current
    if (!pending || tabs.open.includes(pending.name)) return
    pendingWindow.current = null
    const timer = window.setTimeout(() => {
      try {
        pending.win.location.replace(pending.url)
      } catch {
        // The user closed the blank window before it got its URL. Nothing
        // to recover; the target is still listed and still openable.
      }
    }, HANDOFF_DELAY_MS)
    return () => window.clearTimeout(timer)
  }, [tabs.open])

  // Open a target in its own window. When that target is open as a tab,
  // this is a *move*, not a second session: two clients on one target fight
  // over the desktop size in `remote` resize mode, and the embed - being the
  // smaller of the two - keeps winning, which is why a maximised new window
  // used to stay stuck at the iframe's size.
  //
  // The window has to be opened synchronously here or the popup blocker
  // eats it, so it is opened blank inside the click and navigated later,
  // once the embed is actually gone (the effect above).
  function openInNewWindow(info: VncTargetInfo) {
    const { origin } = resolveViewerOrigin(info)
    if (!origin) return
    // A host (code-server's webview) sandboxes its frames without
    // allow-popups, so window.open is refused there outright. The host opens
    // the window instead - and does the same handoff, closing its own view
    // of this target first.
    if (HOST_MODE) {
      notifyEmbedParent('vnc-open-window', { name: info.name, url: origin + info.viewerPath })
      return
    }
    const win = window.open('about:blank', '_blank')
    if (!win) {
      setError('팝업이 차단되어 새 창을 열지 못했습니다 - 이 사이트의 팝업을 허용해주세요.')
      return
    }
    const url = origin + info.viewerPath
    if (tabs.open.includes(info.name)) {
      pendingWindow.current = { name: info.name, win, url }
      closeTab(info.name)
      return
    }
    win.location.replace(url)
  }

  // Keep the tab bar's fullscreen button honest regardless of who toggled
  // fullscreen - it can just as well be noVNC's control bar, or Esc. The
  // active viewer's inner document is the one that owns it on the delegated
  // path below, so it's listened to as well.
  useEffect(() => {
    const sync = () => setFullscreen(isFullscreenActive(frames.current.get(tabs.active)))
    const inner = novncDocument(frames.current.get(tabs.active))
    document.addEventListener('fullscreenchange', sync)
    inner?.addEventListener('fullscreenchange', sync)
    sync()
    return () => {
      document.removeEventListener('fullscreenchange', sync)
      inner?.removeEventListener('fullscreenchange', sync)
    }
  }, [tabs.active, frameLoads])

  // Fallback: fullscreen the whole section rather than the bare iframe, so
  // the tab bar - and with it the button that gets back out - stays on
  // screen.
  function requestOwnFullscreen() {
    sectionRef.current?.requestFullscreen?.().catch(() => {
      // Denied by policy (a missing allow= on some ancestor, most likely) -
      // "새 창으로 옮기기" is the working fallback, so there's nothing useful
      // to say beyond not crashing.
    })
  }

  // Delegate to noVNC's own fullscreen button whenever it can be reached,
  // instead of fullscreening from out here. Both fill the screen, but only
  // the delegated one leaves noVNC's own UI consistent: fullscreening an
  // <iframe> element from the parent never sets document.fullscreenElement
  // *inside* that frame, so noVNC's fullscreenchange handler never fires,
  // its control-bar button stays un-selected, and pressing it then enters a
  // second fullscreen instead of leaving - which is why getting back out
  // used to take opening "＞" and pressing that button twice. With a single
  // owner (noVNC's own document) one press of either control toggles it.
  function handleFullscreen() {
    const frame = frames.current.get(tabs.active)
    if (!frame) {
      if (document.fullscreenElement) document.exitFullscreen?.()
      else requestOwnFullscreen()
      return
    }

    const button = novncDocument(frame)?.getElementById('noVNC_fullscreen_button')
    if (button) {
      const leaving = isFullscreenActive(frame)
      button.click()
      // noVNC swallows a refused request, so an entry that didn't take is
      // detected by looking rather than by catching. On the way in only:
      // on the way out "nothing is fullscreen" is the success case, and
      // re-entering there would be exactly wrong.
      if (!leaving) {
        window.setTimeout(() => {
          if (!isFullscreenActive(frames.current.get(tabs.active))) requestOwnFullscreen()
        }, 300)
      }
      return
    }

    if (isFullscreenActive(frame)) {
      document.exitFullscreen?.()
      return
    }
    requestOwnFullscreen()
  }

  const load = useCallback(async () => {
    try {
      const data = await api.get<VncTargetsResponse>('/targets')
      setTargets(data.targets)
      // Names and labels only - what a host needs to offer a pick list and
      // title its tabs, without being able to reach this API itself.
      notifyEmbedParent('vnc-targets', { targets: data.targets.map((t) => ({ name: t.name, label: t.label })) })
      setBackends(data.backends)
      setError(null)
      // A tab restored from storage (or deep-linked) for a target that no
      // longer exists has nothing to show - drop it rather than keep an
      // empty tab around.
      const names = new Set(data.targets.map((t) => t.name))
      setTabs((prev) => {
        const open = prev.open.filter((n) => names.has(n))
        if (open.length === prev.open.length) return prev
        return { open, active: open.includes(prev.active) ? prev.active : HOME_TAB }
      })
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      withViewTransition(() => setLoading(false))
    }
  }, [setTabs])

  useEffect(() => {
    load()
  }, [load])

  function showNotice(message: string) {
    setNotice(message)
    setTimeout(() => setNotice(null), 2500)
  }

  async function handleSave(target: VncTarget) {
    const editing = dialog?.target
    setSubmitting(true)
    setDialogError(null)
    try {
      if (editing) {
        await api.put(`/targets/${encodeURIComponent(editing.name)}`, target)
      } else {
        await api.post('/targets', target)
      }
      setDialog(null)
      // A rename moves the viewer's own path, so an open tab for the old
      // name would keep pointing at a route that no longer exists.
      if (editing && editing.name !== target.name) {
        setTabs((prev) => ({
          open: prev.open.map((n) => (n === editing.name ? target.name : n)),
          active: prev.active === editing.name ? target.name : prev.active,
        }))
      }
      if (editing && expandedClients === editing.name) setExpandedClients(target.name)
      await load()
      showNotice('저장됨 (App Route도 함께 반영됨)')
    } catch (e) {
      setDialogError(errorMessage(e))
    } finally {
      setSubmitting(false)
    }
  }

  async function handleDelete(name: string) {
    setDeleting(true)
    try {
      await api.del(`/targets/${encodeURIComponent(name)}`)
      closeTab(name)
      if (expandedClients === name) setExpandedClients(null)
      await load()
      showNotice('삭제됨 (App Route도 함께 삭제됨)')
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setDeleting(false)
      setConfirmDelete(null)
    }
  }

  // What an open tab's pane shows: the viewer, or why it can't.
  function renderPane(name: string) {
    const info = targets.find((t) => t.name === name)
    if (!info) return loading ? <Skeleton /> : null
    const { origin, loading: originLoading } = resolveViewerOrigin(info)
    if (originLoading) return <Skeleton />
    if (!origin) {
      return (
        <PaneNotice>
          <span>
            이 페이지는 <code>ROUTER_MANAGER_HOSTS</code> 전용 도메인에서 열려 있습니다. 그 도메인은 보안상
            router-manager 자신만 서비스하고 <code>/app/</code>은 서비스하지 않으므로(사용자가 등록한 앱을
            router-manager와 같은 origin에 두지 않기 위한 의도적인 설계) 뷰어를 띄우려면 공유 호스트네임의
            origin을 알려줘야 합니다 — <code>.env.router</code>에{' '}
            <code>ROUTER_APP_ORIGIN=https://&lt;공유 호스트네임&gt;</code>을 설정하고 router를 재시작하면 이
            탭에서도 바로 볼 수 있습니다. 그때까지는 webmanager의 VNC 탭에서 열거나, 공유 호스트네임의{' '}
            <code>/router/</code> 경로로 접속하세요.
          </span>
        </PaneNotice>
      )
    }
    if (info.viewerOrigin === 'self' && authStatus?.required && !authStatus.unlocked) {
      return (
        <PaneNotice>
          <span>
            router-manager가 잠겨 있어 뷰어가 대상에 연결할 수 없습니다 — 이 백엔드는 App Routes/tinyauth가
            아니라 router-manager 자신의 비밀번호로 보호됩니다. (webmanager 안에서 보고 있다면 webmanager의
            잠금 해제와는 별개입니다.)
          </span>
          <button type="button" className="btn btn-small" onClick={handleUnlockClick}>
            router-manager 잠금 해제
          </button>
        </PaneNotice>
      )
    }
    // Same gate, different reason: since the 2026-09-07 security review
    // (finding C2) RequirePassword is fail-closed, so with NO password
    // configured at all the bridge answers 503 rather than passing through -
    // noVNC only ever surfaces that as "Connection closed (code: 1006)",
    // which reads like the target being down.
    if (info.viewerOrigin === 'self' && authStatus && !authStatus.required) {
      return (
        <PaneNotice>
          <span>
            router-manager 비밀번호가 아직 설정되지 않아 뷰어가 대상에 연결할 수 없습니다 — 이 백엔드의
            WebSocket 브리지는 router-manager 자신의 비밀번호로 보호되며, 비밀번호가 없으면 통과가 아니라
            거부(503)입니다. <strong>설정</strong> 탭에서 비밀번호를 정하거나 <code>.env.router</code>의{' '}
            <code>ROUTER_MANAGER_AUTH_PASSWORD_HASH</code>를 설정한 뒤 다시 여세요.
          </span>
        </PaneNotice>
      )
    }
    // Keyed on the full src so a target whose path or origin changes
    // remounts rather than mutating a connected session's src in place
    // (noVNC reconnects to whatever it was told at load time; mutating src
    // leaves its internal state half-torn-down).
    const src = origin + info.viewerPath
    return (
      <iframe
        key={src}
        ref={(el) => {
          if (el) frames.current.set(name, el)
          else frames.current.delete(name)
        }}
        src={src}
        className="vnc-viewer-frame"
        title={`VNC: ${tabLabel(info, name)}`}
        allow="fullscreen; clipboard-read; clipboard-write"
        onLoad={() => setFrameLoads((n) => n + 1)}
      />
    )
  }

  const activeInfo = targets.find((t) => t.name === tabs.active)

  return (
    <section className="vnc-section" ref={sectionRef}>
      {!HOST_MODE && (
        <VncTabs
          open={tabs.open}
          active={tabs.active}
          targets={targets}
          fullscreen={fullscreen}
          onSelect={(name) => setTabs((prev) => ({ ...prev, active: name }))}
          onClose={closeTab}
          onReorder={(open) => setTabs((prev) => ({ ...prev, open }))}
          onFullscreen={handleFullscreen}
          onMoveToWindow={() => activeInfo && openInNewWindow(activeInfo)}
        />
      )}
      {/* Every pane is laid out at the same size and stacked; only the active
          one is visible. Inactive viewers stay connected (switching tabs is
          not closing them) and, just as importantly, keep their size - with
          display:none a remote-resize viewer would ask its target to become
          0x0 and then back again on every switch. */}
      <div className="vnc-panes">
        <div className={`vnc-pane vnc-home${tabs.active === HOME_TAB ? '' : ' vnc-pane-hidden'}`}>
          <div className="section-header">
            <h1>VNC</h1>
          </div>
          <p className="section-description">
            router에 붙어 있는 GUI 컨테이너의 화면을 브라우저에서 바로 보고 조작합니다. 기본 방식(RFB)은 router가
            noVNC 뷰어를 직접 서비스하고 대상의 raw RFB 포트로 중계하는 것이라, 대상은 VNC만 할 줄 알면 되고 웹
            서버가 필요 없습니다. 대상이 이미 자기 웹 VNC 프런트엔드를 돌리고 있다면 그걸 App Route로 태우는 예전
            방식도 그대로 쓸 수 있습니다. 여러 대상을 탭으로 함께 열어 둘 수 있고, 탭을 닫으면 연결도 끊깁니다.
          </p>

          <div className="card">
            <div className="info-note">
              <span aria-hidden="true">ℹ</span>
              <span>
                대상 주소의 포트는 백엔드에 따라 다릅니다 — <strong>RFB</strong>는 raw RFB 포트(보통{' '}
                <code>5900</code>), <strong>대상이 서비스하는 웹 VNC</strong>는 그 웹 포트(보통 <code>6080</code>)를
                받습니다. 어느 쪽이든 대상 호스트는 App Routes와 같은 allowlist를 통과해야 하며, sibling 프로젝트의
                컨테이너를 추가하려면 <code>ROUTER_EXTRA_ALLOWED_TARGET_HOSTS</code>에 그 호스트를 넣어야 합니다.
                브라우저가 아니라 네이티브 VNC 클라이언트로 붙고 싶다면 <strong>Net 관리</strong> 탭의 Forwards를
                쓰세요.
              </span>
            </div>

            {error && <ErrorBanner message={error} onDismiss={() => setError(null)} />}
            {notice && <p className="success-note">{notice}</p>}

            {loading ? (
              <Skeleton />
            ) : targets.length === 0 ? (
              <p className="empty-state">등록된 VNC 대상이 없습니다.</p>
            ) : (
              <div className="table-wrapper">
                <table className="vnc-table">
                  <thead>
                    <tr>
                      <th>이름</th>
                      <th>대상</th>
                      <th>백엔드</th>
                      <th>창 크기</th>
                      <th>인증</th>
                      <th>연결</th>
                      <th aria-label="동작" className="table-actions-col" />
                    </tr>
                  </thead>
                  <tbody>
                    {targets.map((info) => {
                      const isOpen = tabs.open.includes(info.name)
                      return (
                        <Fragment key={info.name}>
                          <tr>
                            <td>
                              <div className="vnc-name-cell">
                                <span>{info.label || info.name}</span>
                                {/* Only an App-Route-backed target actually answers at
                                    /app/<name>/ - showing that path for a router-side
                                    one would name a URL that 404s. */}
                                {info.viewerOrigin === 'app' && <code>/app/{info.name}/</code>}
                                {info.routeMissing && (
                                  <span className="vnc-warn">App Route 없음 — 편집 후 저장하면 다시 만들어집니다</span>
                                )}
                                {info.routeDiverged && (
                                  <span className="vnc-warn">App Route가 다른 곳을 가리킵니다 — App Routes 탭 확인 필요</span>
                                )}
                              </div>
                            </td>
                            <td>
                              <code>{info.target}</code>
                            </td>
                            <td>{BACKEND_LABEL[info.backend] ?? info.backend}</td>
                            <td>{RESIZE_LABEL[info.resizeMode] ?? info.resizeMode}</td>
                            <td>
                              {/* A router-side backend has no App Route for tinyauth to sit in
                                  front of - router-manager's own password gates its socket
                                  instead, so "없음" would be actively misleading here. */}
                              {info.viewerOrigin === 'self' ? 'router 잠금' : info.requireAuth ? 'tinyauth' : '없음'}
                            </td>
                            <td>
                              <VncClientsBadge
                                count={vncClients[info.name]?.length ?? 0}
                                expanded={expandedClients === info.name}
                                onClick={() => setExpandedClients(expandedClients === info.name ? null : info.name)}
                              />
                            </td>
                            <td className="table-actions-col">
                              <button
                                type="button"
                                className="btn btn-primary btn-small"
                                disabled={info.routeMissing}
                                onClick={() => openTab(info.name)}
                              >
                                {isOpen && !HOST_MODE ? '탭으로 이동' : '열기'}
                              </button>{' '}
                              {/* Deliberately available without opening a tab first: a
                                  full-size desktop is often the *only* way someone
                                  wants to look at a target, and making them open the
                                  embed first just to reach this button meant the
                                  window inherited a session that then fought it over
                                  the desktop size. */}
                              <button
                                type="button"
                                className="btn btn-small"
                                disabled={info.routeMissing}
                                onClick={() => openInNewWindow(info)}
                              >
                                <ExternalLink size={14} aria-hidden="true" /> {isOpen ? '새 창으로 옮기기' : '새 창'}
                              </button>{' '}
                              <button
                                type="button"
                                className="btn btn-small"
                                onClick={() => {
                                  setDialogError(null)
                                  setDialog({ target: info })
                                }}
                              >
                                편집
                              </button>{' '}
                              <button
                                type="button"
                                className="btn btn-danger btn-small"
                                onClick={() => setConfirmDelete(info.name)}
                              >
                                삭제
                              </button>
                            </td>
                          </tr>
                          {expandedClients === info.name && (
                            <tr className="vnc-clients-row">
                              <td colSpan={7}>
                                <VncClientsPanel
                                  name={info.name}
                                  clients={vncClients[info.name] ?? []}
                                  onChanged={refreshVncClients}
                                />
                              </td>
                            </tr>
                          )}
                        </Fragment>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}

            <div className="vnc-add-row">
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => {
                  setDialogError(null)
                  setDialog({ target: null })
                }}
              >
                대상 추가
              </button>
            </div>
          </div>
        </div>

        {tabs.open.map((name) => (
          <div key={name} className={`vnc-pane${tabs.active === name ? '' : ' vnc-pane-hidden'}`}>
            {renderPane(name)}
          </div>
        ))}
      </div>

      {dialog && (
        <TargetDialog
          target={dialog.target}
          backends={backends}
          submitting={submitting}
          error={dialogError}
          onCancel={() => setDialog(null)}
          onSave={handleSave}
        />
      )}

      <ConfirmDialog
        open={confirmDelete !== null}
        onClose={() => setConfirmDelete(null)}
        onConfirm={() => confirmDelete !== null && handleDelete(confirmDelete)}
        title="VNC 대상 삭제"
        confirmLabel="삭제"
        busy={deleting}
      >
        &quot;{confirmDelete}&quot; 대상과 그에 딸린 App Route를 함께 삭제합니다. 대상 컨테이너 자체는 그대로
        남습니다.
      </ConfirmDialog>
    </section>
  )
}
