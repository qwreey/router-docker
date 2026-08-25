import { useCallback, useEffect, useRef, useState } from 'react'
import { Maximize2, ExternalLink } from 'lucide-react'
import { vncApi as api, errorMessage } from '../../api/client'
import type { VncTarget, VncTargetInfo, VncTargetsResponse } from '../../api/types'
import { ErrorBanner } from '../common/ErrorBanner'
import { Skeleton } from '../common/Skeleton'
import { ConfirmDialog } from '../common/ConfirmDialog'
import { withViewTransition } from '../../utils/viewTransition'
import { TargetDialog } from './TargetDialog'
import { useViewerOrigin } from './useViewerOrigin'
import './Vnc.css'

const BACKEND_LABEL: Record<string, string> = {
  novnc: 'noVNC',
}

// '' is a target stored before resizeMode existed; the backend resolves it
// to remote, so the list says the same thing the viewer will actually do.
const RESIZE_LABEL: Record<string, string> = {
  '': '원격 해상도',
  remote: '원격 해상도',
  scale: '맞춰 축소',
  off: '안 함',
}

// The live embed. Deliberately keyed on the full src by the caller, so
// switching targets remounts rather than reusing a connected session's
// iframe (noVNC reconnects to whatever it was told at load time; mutating
// src in place leaves its internal state half-torn-down).
function Viewer({ info, origin, onClose }: { info: VncTargetInfo; origin: string; onClose: () => void }) {
  const frameRef = useRef<HTMLIFrameElement>(null)
  const src = origin + info.viewerPath

  // Fullscreen is requested on the iframe element itself rather than
  // handed to noVNC's own in-page fullscreen button: that button only sees
  // the innermost document, so in the webmanager embed (webmanager ->
  // router -> viewer, two nested cross-origin iframes) it can only fill its
  // own frame. Requesting it here escapes to the real viewport - provided
  // every ancestor iframe carries allow="fullscreen", which is why
  // RouterFrame.tsx on the webmanager side sets it too.
  function handleFullscreen() {
    frameRef.current?.requestFullscreen?.().catch(() => {
      // Denied by policy (a missing allow= on some ancestor, most likely) -
      // the 새 탭 button below is the working fallback, so there's nothing
      // useful to say beyond not crashing.
    })
  }

  return (
    <div className="card vnc-viewer">
      <div className="vnc-viewer-header">
        <div className="vnc-viewer-title">
          <strong>{info.label || info.name}</strong>
          <code>{info.target}</code>
        </div>
        <div className="vnc-viewer-actions">
          <button type="button" className="btn btn-small" onClick={handleFullscreen}>
            <Maximize2 size={14} aria-hidden="true" /> 전체화면
          </button>
          <a className="btn btn-small" href={src} target="_blank" rel="noreferrer">
            <ExternalLink size={14} aria-hidden="true" /> 새 탭
          </a>
          <button type="button" className="btn btn-small" onClick={onClose}>
            닫기
          </button>
        </div>
      </div>
      <iframe
        ref={frameRef}
        src={src}
        className="vnc-viewer-frame"
        title={`VNC: ${info.label || info.name}`}
        allow="fullscreen; clipboard-read; clipboard-write"
      />
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
  const [openName, setOpenName] = useState<string | null>(null)

  const { origin: viewerOrigin, loading: originLoading } = useViewerOrigin()

  const load = useCallback(async () => {
    try {
      const data = await api.get<VncTargetsResponse>('/targets')
      setTargets(data.targets)
      setBackends(data.backends)
      setError(null)
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      withViewTransition(() => setLoading(false))
    }
  }, [])

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
      // A rename moves the viewer's own path, so an open viewer for the old
      // name would keep pointing at a route that no longer exists.
      if (editing && openName === editing.name) setOpenName(target.name)
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
      if (openName === name) setOpenName(null)
      await load()
      showNotice('삭제됨 (App Route도 함께 삭제됨)')
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setDeleting(false)
      setConfirmDelete(null)
    }
  }

  const openTarget = targets.find((t) => t.name === openName) ?? null
  const viewerBlocked = !originLoading && viewerOrigin === null

  return (
    <section>
      <div className="section-header">
        <h1>VNC</h1>
      </div>
      <p className="section-description">
        router에 붙어 있는 GUI 컨테이너의 화면을 브라우저에서 바로 보고 조작합니다. 대상 컨테이너가 띄운 웹 VNC
        프런트엔드(noVNC/websockify)를 App Route로 태워 여기 임베드하는 방식이라, 실제 중계는 App Routes와 완전히
        같은 경로를 씁니다 — 이 탭은 그 위에 뷰어와 대상 목록만 얹습니다.
      </p>

      <div className="card">
        <div className="info-note">
          <span aria-hidden="true">ℹ</span>
          <span>
            대상 주소는 raw RFB 포트(<code>5900</code>)가 아니라 <strong>웹 VNC 포트</strong>(noVNC/websockify,
            보통 <code>6080</code>)여야 합니다. router의 Caddy는 HTTP/WebSocket만 중계할 수 있어 raw RFB는 태울 수
            없습니다 — 네이티브 VNC 클라이언트로 붙고 싶다면 대신 <strong>Net 관리</strong> 탭의 Forwards를
            쓰세요. 대상 호스트는 App Routes와 같은 allowlist를 통과해야 하며, sibling 프로젝트의 컨테이너를
            추가하려면 <code>ROUTER_EXTRA_ALLOWED_TARGET_HOSTS</code>에 그 호스트를 넣어야 합니다.
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
                  <th aria-label="동작" className="table-actions-col" />
                </tr>
              </thead>
              <tbody>
                {targets.map((info) => (
                  <tr key={info.name}>
                    <td>
                      <div className="vnc-name-cell">
                        <span>{info.label || info.name}</span>
                        <code>/app/{info.name}/</code>
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
                    <td>{info.requireAuth ? '요구' : '없음'}</td>
                    <td className="table-actions-col">
                      <button
                        type="button"
                        className="btn btn-primary btn-small"
                        disabled={info.routeMissing}
                        onClick={() => setOpenName(openName === info.name ? null : info.name)}
                      >
                        {openName === info.name ? '닫기' : '보기'}
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
                ))}
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

      {viewerBlocked && openTarget && (
        <div className="card">
          <div className="info-note">
            <span aria-hidden="true">⚠</span>
            <span>
              이 페이지는 <code>ROUTER_MANAGER_HOSTS</code> 전용 도메인에서 열려 있습니다. 그 도메인은 보안상
              router-manager 자신만 서비스하고 <code>/app/</code>은 서비스하지 않으므로(사용자가 등록한 앱을
              router-manager와 같은 origin에 두지 않기 위한 의도적인 설계) 여기서는 뷰어를 띄울 수 없습니다.
              webmanager의 VNC 탭에서 열거나, 공유 호스트네임의 <code>/router/</code> 경로로 접속하세요.
            </span>
          </div>
        </div>
      )}

      {openTarget && viewerOrigin && (
        <Viewer
          key={viewerOrigin + openTarget.viewerPath}
          info={openTarget}
          origin={viewerOrigin}
          onClose={() => setOpenName(null)}
        />
      )}

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
