import { useEffect, useRef, useState } from 'react'
import { ExternalLink, List, Maximize2, Minimize2, X } from 'lucide-react'
import type { VncTargetInfo } from '../../api/types'

// Sentinel for the target list - a virtual, always-present, unclosable first
// tab, same idea as webmanager's TerminalTabs HOME_TAB_ID.
export const HOME_TAB = '__home__'

export function tabLabel(info: VncTargetInfo | undefined, name: string): string {
  return info ? info.label || info.name : name
}

// Visual sibling of webmanager's TerminalTabs (hand-kept, like the rest of
// this frontend's shared-looking pieces - router builds standalone): same
// tab shape, horizontal scroll and drag-reorder, but no rename and no pin.
// A tab's name is always the target's configured label, and there's nothing
// to keep alive past a close - closing a tab is disconnecting.
export function VncTabs({
  open,
  active,
  targets,
  fullscreen,
  onSelect,
  onClose,
  onReorder,
  onFullscreen,
  onMoveToWindow,
}: {
  open: string[]
  active: string
  targets: VncTargetInfo[]
  fullscreen: boolean
  onSelect: (name: string) => void
  onClose: (name: string) => void
  onReorder: (next: string[]) => void
  onFullscreen: () => void
  onMoveToWindow: () => void
}) {
  const listRef = useRef<HTMLDivElement | null>(null)
  const dragRef = useRef<string | null>(null)
  const [dragOver, setDragOver] = useState<string | null>(null)

  useEffect(() => {
    listRef.current?.querySelector('.vnc-tab-active')?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }, [active])

  function handleDrop(target: string) {
    const dragged = dragRef.current
    dragRef.current = null
    setDragOver(null)
    if (!dragged || dragged === target) return
    const next = [...open]
    const from = next.indexOf(dragged)
    const to = next.indexOf(target)
    if (from === -1 || to === -1) return
    next.splice(to, 0, ...next.splice(from, 1))
    onReorder(next)
  }

  const byName = new Map(targets.map((t) => [t.name, t]))

  return (
    <div className="vnc-tabbar">
      <div className="vnc-tab-list" role="tablist" aria-label="VNC 대상" ref={listRef}>
        <div className={`vnc-tab${active === HOME_TAB ? ' vnc-tab-active' : ''}`} role="tab" aria-selected={active === HOME_TAB}>
          <button type="button" className="vnc-tab-label" onClick={() => onSelect(HOME_TAB)}>
            <List size={13} aria-hidden="true" />
            대상 목록
          </button>
        </div>
        {open.map((name) => {
          const info = byName.get(name)
          const label = tabLabel(info, name)
          return (
            <div
              key={name}
              className={
                'vnc-tab' + (name === active ? ' vnc-tab-active' : '') + (dragOver === name ? ' vnc-tab-drag-over' : '')
              }
              role="tab"
              aria-selected={name === active}
              draggable
              onDragStart={() => {
                dragRef.current = name
              }}
              onDragOver={(e) => {
                e.preventDefault()
                if (dragOver !== name) setDragOver(name)
              }}
              onDragLeave={() => setDragOver((prev) => (prev === name ? null : prev))}
              onDrop={(e) => {
                e.preventDefault()
                handleDrop(name)
              }}
              onDragEnd={() => {
                dragRef.current = null
                setDragOver(null)
              }}
            >
              <button
                type="button"
                className="vnc-tab-label"
                onClick={() => onSelect(name)}
                title={info ? info.target : undefined}
              >
                {label}
              </button>
              <button
                type="button"
                className="vnc-tab-close"
                onClick={() => onClose(name)}
                title="닫기 (연결 끊기)"
                aria-label={`${label} 닫기`}
              >
                <X size={13} />
              </button>
            </div>
          )
        })}
      </div>
      {/* Act on the active viewer only, so they're only there while one is.
          They used to sit in each viewer's own header row; one row of chrome
          instead of two is most of what full-bleed buys. */}
      {active !== HOME_TAB && (
        <div className="vnc-tab-actions">
          <button
            type="button"
            className="vnc-tab-action"
            onClick={onFullscreen}
            title={fullscreen ? '전체화면 해제' : '전체화면'}
            aria-label={fullscreen ? '전체화면 해제' : '전체화면'}
          >
            {fullscreen ? <Minimize2 size={15} /> : <Maximize2 size={15} />}
          </button>
          {/* Hands the session over rather than opening a second one - see
              Vnc.tsx's openInNewWindow. */}
          <button
            type="button"
            className="vnc-tab-action"
            onClick={onMoveToWindow}
            title="새 창으로 옮기기"
            aria-label="새 창으로 옮기기"
          >
            <ExternalLink size={15} />
          </button>
        </div>
      )}
    </div>
  )
}
