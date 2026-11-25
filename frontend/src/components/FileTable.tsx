// FileTable — the middle file list. Rows come from the backend via
// api.browse; this component never invents rows. Click selects (and
// opens the Inspector), double-click enters a folder, checkbox
// toggles multi-select. Clicking an already-selected row deselects it.
import type { Node } from '../mygo'
import { formatSize, formatTime } from '../lib/format'
import { staggerStyle } from './motion'

export default function FileTable({
  nodes,
  selectedId,
  checked,
  onSelect,
  onToggleCheck,
  onOpen,
}: {
  nodes: Node[]
  selectedId: string | null
  checked: Set<string>
  onSelect: (n: Node | null) => void
  onToggleCheck: (n: Node) => void
  onOpen: (n: Node) => void
}) {
  if (nodes.length === 0) {
    return (
      <div className="empty">
        <h2>这个文件夹是空的</h2>
        <p>把文件拖进来，或点右上角上传。</p>
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
              onClick={() => onSelect(isSelected ? null : n)}
              onDoubleClick={() => {
                if (n.isDir) onOpen(n)
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
                  <span aria-hidden>{n.isDir ? '📁' : '📄'}</span>
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
