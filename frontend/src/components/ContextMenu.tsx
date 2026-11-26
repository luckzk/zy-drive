// SPDX-License-Identifier: MIT
// ContextMenu — 文件列表的右键菜单（v0.10.0）。固定定位在光标处，
// 首次渲染后量一次自身尺寸、clamp 到视口内；点外部、按 Esc 或
// 选一项后关闭。样式走 tokens.css 的 .context-menu（复用侧栏
// 浮层菜单的卡片、圆角、阴影与入场动画）。无 emoji。
import { useEffect, useRef, useState } from 'react'

export interface ContextMenuItem {
  label: string
  danger?: boolean
  onClick: () => void
}

export type ContextMenuEntry = ContextMenuItem | 'sep'

export default function ContextMenu({
  x,
  y,
  items,
  onClose,
}: {
  x: number
  y: number
  items: ContextMenuEntry[]
  onClose: () => void
}) {
  const ref = useRef<HTMLDivElement | null>(null)
  const [pos, setPos] = useState({ left: x, top: y })
  // Clamp inside the viewport after measuring the rendered menu.
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const r = el.getBoundingClientRect()
    setPos({
      left: Math.max(4, Math.min(x, window.innerWidth - r.width - 4)),
      top: Math.max(4, Math.min(y, window.innerHeight - r.height - 4)),
    })
  }, [x, y])
  useEffect(() => {
    const onPointer = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose()
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('mousedown', onPointer)
    window.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onPointer)
      window.removeEventListener('keydown', onKey)
    }
  }, [onClose])
  return (
    <div ref={ref} className="context-menu" role="menu" style={{ left: pos.left, top: pos.top }}>
      {items.map((it, i) =>
        it === 'sep' ? (
          <div key={`sep-${i}`} className="context-menu-sep" aria-hidden />
        ) : (
          <button
            key={it.label}
            role="menuitem"
            className={it.danger ? 'danger' : undefined}
            onClick={() => {
              onClose()
              it.onClick()
            }}
          >
            {it.label}
          </button>
        ),
      )}
    </div>
  )
}
