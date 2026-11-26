// SPDX-License-Identifier: MIT
// FileTable — the middle file list. Rows come from the backend via
// api.browse; this component never invents rows. Click selects (and
// opens the Inspector), double-click enters a folder, checkbox
// toggles multi-select. Clicking an already-selected row deselects it.
import type { Node } from '../mygo'
import { formatSize, formatTime } from '../lib/format'
import { staggerStyle } from './motion'
import { FileTypeIcon } from './icons'

export default function FileTable({
  nodes,
  selectedId,
  checked,
  onSelect,
  onToggleCheck,
  onOpen,
  onContextMenu,
  onPreview,
}: {
  nodes: Node[]
  selectedId: string | null
  checked: Set<string>
  onSelect: (n: Node | null) => void
  onToggleCheck: (n: Node) => void
  onOpen: (n: Node) => void
  // Right-click menu (v0.10.0): the parent decides whether the
  // row acts alone or as part of the checked set.
  onContextMenu?: (n: Node, e: React.MouseEvent) => void
  // Fullscreen preview (v0.14.0): double-click / Space on a file.
  onPreview?: (n: Node) => void
}) {
  if (nodes.length === 0) {
    return (
      <div className="empty">
        <h2>这个文件夹是空的</h2>
        <p>把文件拖进来上传。</p>
      </div>
    )
  }
  return (
    <table className="file-table">
      <thead>
        <tr>
          <th style={{ width: 32 }}></th>
          <th>名称</th>
          <th>大小</th>
          <th>修改时间</th>
        </tr>
      </thead>
      <tbody>
        {nodes.map((n, i) => {
          const key = n.ref || n.path
          const isSelected = selectedId === key
          return (
            <tr
              key={key}
              className={`stagger-item${isSelected ? ' selected' : ''}`}
              style={staggerStyle(i)}
              tabIndex={0}
              onClick={() => onSelect(isSelected ? null : n)}
              onDoubleClick={() => {
                if (n.isDir) onOpen(n)
                else onPreview?.(n)
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  if (n.isDir) onOpen(n)
                  else onSelect(n)
                } else if (e.key === ' ') {
                  // Space opens the fullscreen preview for files
                  // (Finder behaviour) — even with a multi-select,
                  // Space still previews (report §四). Folders
                  // ignore it. Guard: the checkbox input is its
                  // own focus target, so Space there still toggles
                  // the checkbox, never the preview.
                  if (n.isDir) return
                  if ((e.target as HTMLElement).tagName === 'INPUT') return
                  e.preventDefault()
                  onPreview?.(n)
                }
              }}
              onContextMenu={(e) => {
                e.preventDefault()
                onContextMenu?.(n, e)
              }}
            >
              <td onClick={(e) => e.stopPropagation()}>
                <input
                  type="checkbox"
                  checked={checked.has(key)}
                  onChange={() => onToggleCheck(n)}
                  aria-label={`选择 ${n.name}`}
                />
              </td>
              <td>
                <span className="row-name">
                  <span className="file-icon" aria-hidden>
                    <FileTypeIcon name={n.name} isDir={n.isDir} />
                  </span>
                  <span>{n.name}</span>
                  {n.isDir && <span className="muted">›</span>}
                </span>
              </td>
              <td>{n.isDir ? '—' : formatSize(n.size)}</td>
              <td className="muted">{formatTime(n.modTime)}</td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}
