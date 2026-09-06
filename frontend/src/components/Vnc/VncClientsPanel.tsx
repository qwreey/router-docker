import { useState } from 'react'
import { Users } from 'lucide-react'
import { vncApi as api, errorMessage } from '../../api/client'
import type { VncClientInfo } from '../../api/types'
import { ConfirmDialog } from '../common/ConfirmDialog'
import { vncClientLabel, vncConnectedFor } from './vncClientLabel'

// The badge cell for one target row. Only ever meaningful for a BackendRFB
// target (see handleListVncClients's own comment) - a BackendNoVNC target
// always polls to an empty list, so this renders the same "0" badge for it
// as it would for an idle rfb target. That's fine: it isn't claiming
// anything false, just not claiming anything useful either.
export function VncClientsBadge({
  count,
  expanded,
  onClick,
}: {
  count: number
  expanded: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      className={`badge ${count > 0 ? 'badge-green' : 'badge-gray'} vnc-clients-badge`}
      onClick={onClick}
      title="이 대상에 연결된 클라이언트 보기"
    >
      <Users size={12} aria-hidden="true" /> {count}
      {expanded ? ' ▲' : ' ▼'}
    </button>
  )
}

// The expanded row's content: per-client IP/브라우저/접속 시간 plus a 끊기
// button, and the honesty note about what this list can and can't see.
// Rendered by Vnc.tsx inside its own full-width <tr> when a target's badge
// is toggled open.
export function VncClientsPanel({
  name,
  clients,
  onChanged,
}: {
  name: string
  clients: VncClientInfo[]
  onChanged: () => void
}) {
  const [confirmKick, setConfirmKick] = useState<VncClientInfo | null>(null)
  const [kicking, setKicking] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleKick(client: VncClientInfo) {
    setKicking(true)
    setError(null)
    try {
      await api.del(`/targets/${encodeURIComponent(name)}/clients/${client.id}`)
      setConfirmKick(null)
      onChanged()
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setKicking(false)
    }
  }

  return (
    <div className="vnc-clients-panel">
      <p className="vnc-hint">
        여기 나오는 목록은 router의 rfb 중계를 거친 연결만입니다 — 대상의 raw RFB 포트에 직접 붙은 네이티브
        VNC 클라이언트는 이 목록에 나타나지 않고, 여기서 끊을 수도 없습니다. 또한 IP는 router 앞단 nginx가
        알려준 값이라, router 앞에 또 다른 리버스 프록시를 뒀는데 그쪽이 클라이언트 IP를 넘겨주지 않는다면
        여기 보이는 IP는 실제 클라이언트가 아니라 그 프록시의 IP일 수 있습니다.
      </p>
      {error && <p className="vnc-hint vnc-hint-warn">{error}</p>}
      {clients.length === 0 ? (
        <p className="empty-state">router를 거쳐 연결된 클라이언트가 없습니다.</p>
      ) : (
        <table className="vnc-table vnc-clients-table">
          <thead>
            <tr>
              <th>IP</th>
              <th>클라이언트</th>
              <th>접속 시간</th>
              <th aria-label="동작" className="table-actions-col" />
            </tr>
          </thead>
          <tbody>
            {clients.map((client) => (
              <tr key={client.id}>
                <td>
                  <code>{client.remoteIp}</code>
                </td>
                <td>{vncClientLabel(client.userAgent)}</td>
                <td>{vncConnectedFor(client.connectedAt)}</td>
                <td className="table-actions-col">
                  <button type="button" className="btn btn-danger btn-small" onClick={() => setConfirmKick(client)}>
                    끊기
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <ConfirmDialog
        open={confirmKick !== null}
        onClose={() => setConfirmKick(null)}
        onConfirm={() => confirmKick && handleKick(confirmKick)}
        title="클라이언트 연결 끊기"
        confirmLabel="끊기"
        busy={kicking}
      >
        {confirmKick && (
          <>
            <code>{confirmKick.remoteIp}</code> ({vncClientLabel(confirmKick.userAgent)})의 화면 연결을 끊습니다.
            그 클라이언트가 열어둔 뷰어가 자동으로 재접속하지 못하도록 잠시 동안 재연결도 함께 막습니다 — 브라우저
            탭 자체를 강제로 닫지는 못합니다.
          </>
        )}
      </ConfirmDialog>
    </div>
  )
}
