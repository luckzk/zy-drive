// CommandPalette — Ctrl+K. Searches the active mount for real via
// api.search; when there is no mount or no query it says so. It never
// shows invented results.
import { useState } from 'react'
import type { Mount } from '../mygo'
import { api, friendlyError } from '../api/client'
import { useClosing } from './motion'
import { FileTypeIcon } from './icons'

export default function CommandPalette({
  activeMount,
  onClose,
  onPick,
}: {
  activeMount: Mount | null
  onClose: () => void
  onPick: (mount: Mount, ref: string) => void
}) {
  const [closing, requestClose] = useClosing(onClose)
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<{ name: string; path: string; ref: string; isDir: boolean }[]>([])
  const [localOnly, setLocalOnly] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [searching, setSearching] = useState(false)

  const run = async () => {
    const q = query.trim()
    if (!activeMount || !q) return
    setSearching(true)
    setError(null)
    try {
      const res = await api.search(activeMount.id, q)
      setResults((res?.nodes ?? []).map((n) => ({ name: n.name, path: n.path, ref: n.ref || n.path, isDir: n.isDir })))
      setLocalOnly(res?.localOnly ?? false)
    } catch (e) {
      setError(friendlyError(e))
      setResults([])
    } finally {
      setSearching(false)
    }
  }

  return (
    <div className={`dialog-backdrop${closing ? ' closing' : ''}`} onClick={requestClose}>
      <div className={`palette${closing ? ' closing' : ''}`} onClick={(e) => e.stopPropagation()}>
        <input
          className="input"
          autoFocus
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') void run()
            if (e.key === 'Escape') requestClose()
          }}
          placeholder={activeMount ? `在 ${activeMount.displayName} 里搜索…` : '先添加一个网盘才能搜索'}
        />
        <div style={{ marginTop: 12 }}>
          {!activeMount && <p className="muted">还没有网盘。先添加网盘，再回来搜索。</p>}
          {activeMount && !query.trim() && <p className="muted">输入关键词后按回车搜索。上传、下载、新建同步任务在各自的页面里操作。</p>}
          {searching && <p className="muted">搜索中…</p>}
          {error && <div className="error-box">{error}</div>}
          {localOnly && <p className="muted">本地结果（该网盘不支持服务端搜索）。</p>}
          {results.map((r) => (
            <div
              key={r.ref}
              className="palette-row"
              onClick={() => {
                onPick(activeMount!, r.ref)
                requestClose()
              }}
            >
              <span className="file-icon" aria-hidden>
                <FileTypeIcon name={r.name} isDir={r.isDir} />
              </span>
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{r.name}</span>
              <span className="muted">· {r.path}</span>
            </div>
          ))}
          {activeMount && query.trim() && !searching && !error && results.length === 0 && <p className="muted">没有找到“{query.trim()}”。</p>}
        </div>
        <div style={{ textAlign: 'right', marginTop: 12 }}>
          <button className="btn" onClick={requestClose}>
            关闭
          </button>
        </div>
      </div>
    </div>
  )
}
