// SPDX-License-Identifier: MIT
// FileGrid — the grid file view (v0.13.0). Same data and same
// behaviour as FileTable (list view), only the layout differs:
// cards with a thumbnail (when the node has one) or a large
// type icon, name, and size + modification time. Click selects
// (and opens the Inspector), double-click / Enter enters a
// folder, checkbox toggles multi-select, right-click opens the
// same context menu as the table. The parent (FileBrowser)
// switches between the two views; the choice persists in
// localStorage (key zydrive.view, default list).
import type { Node } from '../mygo'
import { formatSize, formatTime } from '../lib/format'
import { staggerStyle } from './motion'
import { FileTypeIcon } from './icons'

export default function FileGrid({
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
  // card acts alone or as part of the checked set.
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
    <div className="file-grid">
      {nodes.map((n, i) => {
        const key = n.ref || n.path
        const isSelected = selectedId === key
        return (
          <div
            key={key}
            className={`file-grid-card stagger-item${isSelected ? ' selected' : ''}`}
            style={staggerStyle(i)}
            tabIndex={0}
            aria-label={n.name}
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
            <span className="file-grid-check" onClick={(e) => e.stopPropagation()}>
              <input
                type="checkbox"
                checked={checked.has(key)}
                onChange={() => onToggleCheck(n)}
                aria-label={`选择 ${n.name}`}
              />
            </span>
            <span className="file-grid-thumb" aria-hidden>
              {n.thumbUrl ? (
                <img src={n.thumbUrl} alt="" />
              ) : (
                <FileTypeIcon name={n.name} isDir={n.isDir} size={40} />
              )}
            </span>
            <span className="file-grid-name" title={n.name}>
              {n.name}
            </span>
            <span className="file-grid-meta">
              {n.isDir ? '文件夹' : formatSize(n.size)} · {formatTime(n.modTime)}
            </span>
          </div>
        )
      })}
    </div>
  )
}
