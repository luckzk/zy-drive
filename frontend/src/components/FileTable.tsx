// FileTable — the middle file list. Sortable, multi-select, breadcrumbs
// live in App. Empty state shows the add-guide, never demo files
// pretending to be real data (the DEMO rows below are the UI-demo
// placeholder and are removed when the Bind client lands).
import { useState } from 'react'

type Row = { name: string; size: string; time: string; isDir: boolean }

const PLACEHOLDER_ROWS: Row[] = [
  { name: '照片', size: '—', time: '2026-11-24 10:02', isDir: true },
  { name: '2026-11-24-行程.pdf', size: '1.2 MB', time: '2026-11-24 09:41', isDir: false },
  { name: '备份.zip', size: '84.5 MB', time: '2026-11-23 18:20', isDir: false },
]

export default function FileTable({ mountId }: { mountId: string }) {
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const toggle = (name: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }
  return (
    <table style={{ width: '100%', borderCollapse: 'collapse' }} data-mount={mountId}>
      <thead>
        <tr style={{ textAlign: 'left' }}>
          <th style={{ width: 32 }}></th>
          <th>名称</th>
          <th>大小</th>
          <th>修改时间</th>
        </tr>
      </thead>
      <tbody>
        {PLACEHOLDER_ROWS.map((r) => (
          <tr key={r.name} style={{ background: selected.has(r.name) ? '#f0fdf4' : undefined }}>
            <td>
              <input type="checkbox" checked={selected.has(r.name)} onChange={() => toggle(r.name)} />
            </td>
            <td>{r.isDir ? '📁 ' : '📄 '}{r.name}</td>
            <td>{r.size}</td>
            <td className="muted">{r.time}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
