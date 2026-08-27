import { useState, type FormEvent } from 'react'
import { Sheet } from '../common/Sheet'
import { ErrorBanner } from '../common/ErrorBanner'
import type { VncResizeMode, VncTarget } from '../../api/types'

// Same bind-address nudge Dev Proxy's RouteDialog and the App Routes tab
// both carry: router's Caddy reaches targets over code-docker-internal, so
// a target bound to the other container's own loopback is unreachable from
// here no matter what.
function looksUnreachable(target: string) {
  const host = target.split(':')[0]
  return host === '127.0.0.1' || host === 'localhost' || host === ''
}

// Which port to type is the single most likely error in this form, and the
// right answer *inverts* between the two backends: rfb wants the raw RFB
// port (router dials it and speaks RFB itself), novnc wants the target's
// own web VNC port (router reverse-proxies HTTP to it). Getting it backwards
// fails as an opaque proxy/connection error much later, so call it out here.
const RAW_RFB_PORTS = new Set(['5900', '5901', '5902'])
const WEB_VNC_PORTS = new Set(['6080', '6081'])

function portLooksWrong(backend: string, target: string): string | null {
  const port = target.split(':')[1]
  if (port === undefined) return null
  if (backend === 'rfb' && WEB_VNC_PORTS.has(port)) {
    return 'web'
  }
  if (backend !== 'rfb' && RAW_RFB_PORTS.has(port)) {
    return 'rfb'
  }
  return null
}

const BACKEND_LABEL: Record<string, string> = {
  rfb: 'RFB — router가 직접 중계 (권장)',
  novnc: '대상이 서비스하는 웹 VNC',
}

const BACKEND_HINT: Record<string, string> = {
  rfb: 'router가 noVNC 뷰어를 자기 origin에서 직접 서비스하고, 브라우저의 WebSocket을 대상의 raw RFB 포트(보통 5900)로 중계합니다. 대상은 VNC만 할 줄 알면 되고 웹 서버가 필요 없습니다 — 네이티브 클라이언트가 붙는 그 포트 그대로입니다. App Route를 만들지 않으며, 접근 제어는 router-manager 자신의 비밀번호가 담당합니다.',
  novnc: '대상이 이미 웹 VNC 프런트엔드(noVNC/websockify 등)를 돌리고 있고, router는 그걸 App Route로 중계만 합니다. 대상 주소는 그 웹 포트(보통 6080)입니다. raw RFB 포트가 없는 스택(Selkies 등)에는 이 방식만 쓸 수 있습니다.',
}

// Unlike backends (server-supplied, see the picker below), the resize modes
// are a closed set - a mode only exists if a viewer's own query parameter
// accepts it - so they're spelled out here with real labels instead of
// being rendered from a list of bare identifiers.
const RESIZE_MODES: { value: Exclude<VncResizeMode, ''>; label: string }[] = [
  { value: 'remote', label: '원격 해상도 변경 (권장)' },
  { value: 'scale', label: '화면에 맞춰 축소' },
  { value: 'off', label: '아무것도 안 함' },
]

const RESIZE_HINT: Record<Exclude<VncResizeMode, ''>, string> = {
  remote:
    '창 크기가 바뀌면 대상의 실제 해상도를 그만큼 바꿔달라고 요청합니다(RFB SetDesktopSize) — 네이티브 클라이언트에서 창을 늘렸을 때와 같은 동작입니다. wayvnc는 이걸 기본으로 지원하지만, 지원하지 않는 서버(고정 크기 Xvfb 앞의 x11vnc 등)에서는 요청이 거부되고 스케일링도 하지 않아 스크롤바가 생깁니다 — 그 경우 아래 "화면에 맞춰 축소"를 쓰세요.',
  scale: '대상의 해상도는 그대로 두고, 받은 화면을 뷰어 크기에 맞춰 축소해서 보여줍니다.',
  off: '해상도도 안 바꾸고 축소도 하지 않습니다. 뷰어보다 크면 스크롤바가 생깁니다.',
}

export function TargetDialog({
  target: existing,
  backends,
  submitting,
  error,
  onCancel,
  onSave,
}: {
  target: VncTarget | null
  backends: string[]
  submitting: boolean
  error: string | null
  onCancel: () => void
  onSave: (target: VncTarget) => void
}) {
  const [name, setName] = useState(existing?.name ?? '')
  const [label, setLabel] = useState(existing?.label ?? '')
  const [target, setTarget] = useState(existing?.target ?? '')
  const [backend, setBackend] = useState(existing?.backend ?? backends[0] ?? 'rfb')
  const [requireAuth, setRequireAuth] = useState(existing?.requireAuth ?? false)
  // '' (a target predating this field) resolves to the backend's default
  // rather than being offered as a fourth, blank option.
  const [resizeMode, setResizeMode] = useState<Exclude<VncResizeMode, ''>>(
    existing?.resizeMode || 'remote',
  )

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    // requireAuth is forced off for a backend that has no App Route - the
    // API refuses the combination, and a value left over from switching the
    // picker would otherwise make saving fail with a confusing error.
    onSave({
      name: name.trim(),
      label: label.trim(),
      target: target.trim(),
      backend,
      requireAuth: backend === 'rfb' ? false : requireAuth,
      resizeMode,
    })
  }

  return (
    <Sheet open onClose={onCancel} title={existing ? 'VNC 대상 편집' : 'VNC 대상 추가'}>
      <form onSubmit={handleSubmit} className="vnc-form">
        <div className="form-field">
          <label htmlFor="vnc-label">이름 (표시용)</label>
          <input
            id="vnc-label"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder="Roblox Studio"
          />
        </div>
        <div className="form-field">
          <label htmlFor="vnc-name">경로 세그먼트</label>
          <input
            id="vnc-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="studio-vnc"
            pattern="[a-z0-9][a-z0-9-]{0,61}[a-z0-9]|[a-z0-9]"
            required
          />
          <p className="vnc-hint">
            이 대상을 태울 App Route의 이름이자 뷰어가 열리는 경로입니다 — <code>/app/{name || '<이름>'}/</code>.
            같은 이름의 App Route가 자동으로 만들어지고, 여기서 지우면 함께 사라집니다.
          </p>
        </div>
        <div className="form-field">
          <label htmlFor="vnc-target">대상 (host:port)</label>
          <input
            id="vnc-target"
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            placeholder="vnc-only:6080"
            required
          />
          {portLooksWrong(backend, target) === 'rfb' && (
            <p className="vnc-hint vnc-hint-warn">
              <code>{target}</code>의 포트는 raw RFB 포트로 보입니다. 지금 고른 백엔드는 대상이 직접
              서비스하는 <strong>웹 VNC 포트</strong>(보통 <code>6080</code>)를 기대합니다 — raw RFB 포트에
              바로 붙이려면 백엔드를 <strong>RFB</strong>로 바꾸세요.
            </p>
          )}
          {portLooksWrong(backend, target) === 'web' && (
            <p className="vnc-hint vnc-hint-warn">
              <code>{target}</code>의 포트는 웹 VNC(noVNC/websockify) 포트로 보입니다. <strong>RFB</strong>{' '}
              백엔드는 raw RFB 포트(보통 <code>5900</code>)를 기대합니다 — 대상의 웹 프런트엔드를 그대로
              쓰려면 백엔드를 <strong>대상이 서비스하는 웹 VNC</strong>로 바꾸세요.
            </p>
          )}
          {looksUnreachable(target) && (
            <p className="vnc-hint vnc-hint-warn">
              <code>{target || '(비어있음)'}</code>은(는) router 컨테이너 자신을 가리켜 대상 컨테이너에 닿지
              않습니다 — <code>vnc-only:6080</code>처럼 compose 서비스 호스트네임이나 네트워크 별칭을 쓰세요.
            </p>
          )}
        </div>
        <div className="form-field">
          <label htmlFor="vnc-backend">뷰어 백엔드</label>
          <select id="vnc-backend" value={backend} onChange={(e) => setBackend(e.target.value)}>
            {backends.map((b) => (
              <option key={b} value={b}>
                {BACKEND_LABEL[b] ?? b}
              </option>
            ))}
          </select>
          <p className="vnc-hint">{BACKEND_HINT[backend] ?? ''}</p>
        </div>
        <div className="form-field">
          <label htmlFor="vnc-resize">창 크기 변경 처리</label>
          <select
            id="vnc-resize"
            value={resizeMode}
            onChange={(e) => setResizeMode(e.target.value as Exclude<VncResizeMode, ''>)}
          >
            {RESIZE_MODES.map((m) => (
              <option key={m.value} value={m.value}>
                {m.label}
              </option>
            ))}
          </select>
          <p className="vnc-hint">{RESIZE_HINT[resizeMode]}</p>
        </div>
        {/* tinyauth is an App-Routes-level thing (a forward_auth in the app's
            own Caddyfile fragment), so it has nothing to act on for a backend
            that doesn't create one - the backend refuses the combination
            rather than accepting a flag that would do nothing. */}
        {backend === 'rfb' ? (
          <p className="vnc-hint">
            이 백엔드에는 별도의 "인증 요구" 설정이 없습니다 — 뷰어와 그 WebSocket을 router-manager가 직접
            서비스하므로, <strong>router-manager 자신의 비밀번호</strong>(설정 탭)가 그대로 접근 제어가 됩니다.
          </p>
        ) : (
          <>
            <label className="vnc-checkbox-option">
              <input type="checkbox" checked={requireAuth} onChange={(e) => setRequireAuth(e.target.checked)} />
              인증 요구 (tinyauth)
            </label>
            {requireAuth && (
              <p className="vnc-hint">
                tinyauth에 사용자가 최소 한 명 등록되어 있어야 하고(설정 &gt; tinyauth 탭), 로그인 화면을 서비스할{' '}
                <code>TINYAUTH_HOSTS</code>가 <code>.env.router</code>에 설정되어 있어야 합니다. 둘 중 하나라도
                빠지면 이 대상은 로그인 페이지에 도달할 방법이 없어 그대로 접속이 막힙니다.
              </p>
            )}
          </>
        )}
        {error && <ErrorBanner message={error} />}
        <div className="vnc-form-actions">
          <button type="submit" className="btn btn-primary" disabled={submitting}>
            {submitting ? '저장하는 중...' : '저장'}
          </button>
          <button type="button" className="btn" onClick={onCancel} disabled={submitting}>
            취소
          </button>
        </div>
      </form>
    </Sheet>
  )
}
