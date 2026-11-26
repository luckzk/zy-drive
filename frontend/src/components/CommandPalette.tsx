// SPDX-License-Identifier: MIT
// CommandPalette — Ctrl+K. Searches the active mount for real via
// api.search; when there is no mount or no query it says so. It never
// shows invented results.
//
// v0.15.0: live search (150ms debounce, Enter forces an immediate
// search instead of waiting it out), arrow-key navigation (↑/↓ +
// Enter to open), match highlighting (<mark> on the hit substring),
// and size/date per result (the API already returns them). The
// footer is shortcut hints, not a redundant 关闭 button — Esc and
// clicking the mask still close.
import { useEffect, useRef, useState, type ReactNode } from 'react'
import type { Mount } from '../mygo'
import { api, friendlyError } from '../api/client'
import { formatSize, formatTime } from '../lib/format'
import { useClosing } from './motion'
import { FileTypeIcon } from './icons'

interface PaletteResult {
  name: string
  path: string
  ref: string
  isDir: boolean
  size: number
  modTime: string
}

// highlight wraps the matched substring in <mark>
// (case-insensitive; the query is escaped before becoming a RegExp,
// so a query like "a+b" never breaks the pattern).
function highlight(name: string, query: string): ReactNode {
  const q = query.trim()
  if (!q) return name
  const escaped = q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const parts = name.split(new RegExp(`(${escaped})`, 'ig'))
  return parts.map((p, i) =>
    p.toLowerCase() === q.toLowerCase() ? <mark key={i}>{p}</mark> : <span key={i}>{p}</span>,
  )
}

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
  const [results, setResults] = useState<PaletteResult[]>([])
  const [localOnly, setLocalOnly] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [searching, setSearching] = useState(false)
  const [activeIdx, setActiveIdx] = useState(0)
  // Stale-response guard: every search gets a sequence number; a
  // slower earlier response never overwrites a newer one.
  const searchSeq = useRef(0)
  const debounceTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  // The query the last started search actually ran for. Enter opens
  // the highlighted result only when the results match the current
  // input; otherwise it searches immediately instead of opening a
  // stale result.
  const lastSearchedQuery = useRef('')

  const run = async (q: string) => {
    const query = q.trim()
    if (!activeMount || !query) return
    lastSearchedQuery.current = query
    const seq = ++searchSeq.current
    setSearching(true)
    setError(null)
    try {
      const res = await api.search(activeMount.id, query)
      if (seq !== searchSeq.current) return
      setResults(
        (res?.nodes ?? []).map((n) => ({
          name: n.name,
          path: n.path,
          ref: n.ref || n.path,
          isDir: n.isDir,
          size: n.size,
          modTime: n.modTime ?? '',
        })),
      )
      setLocalOnly(res?.localOnly ?? false)
      setActiveIdx(0)
    } catch (e) {
      if (seq !== searchSeq.current) return
      setError(friendlyError(e))
      setResults([])
    } finally {
      if (seq === searchSeq.current) setSearching(false)
    }
  }

  // Live search: 150ms after the user stops typing.
  useEffect(() => {
    if (debounceTimer.current) clearTimeout(debounceTimer.current)
    const q = query.trim()
    if (!activeMount || !q) {
      lastSearchedQuery.current = ''
      setResults([])
      setError(null)
      setSearching(false)
      return
    }
    debounceTimer.current = setTimeout(() => void run(q), 150)
    return () => {
      if (debounceTimer.current) clearTimeout(debounceTimer.current)
    }
    // run reads the query through its argument; only the query
    // (and the mount) restart the timer.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query, activeMount])

  const pickActive = () => {
    const r = results[activeIdx]
    if (!activeMount || !r) return
    onPick(activeMount, r.ref)
    requestClose()
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
            if (e.key === 'ArrowDown') {
              e.preventDefault()
              setActiveIdx((i) => (results.length === 0 ? 0 : Math.min(i + 1, results.length - 1)))
            } else if (e.key === 'ArrowUp') {
              e.preventDefault()
              setActiveIdx((i) => Math.max(i - 1, 0))
            } else if (e.key === 'Enter') {
              if (results.length > 0 && query.trim() === lastSearchedQuery.current) pickActive()
              else void run(query)
            } else if (e.key === 'Escape') requestClose()
          }}
          placeholder={activeMount ? `在 ${activeMount.displayName} 里搜索…` : '先添加一个网盘才能搜索'}
        />
        <div style={{ marginTop: 12 }}>
          {!activeMount && <p className="muted">还没有网盘。先添加网盘，再回来搜索。</p>}
          {activeMount && !query.trim() && <p className="muted">输入关键词即开始搜索。上传、下载、新建同步任务在各自的页面里操作。</p>}
          {searching && <p className="muted">搜索中…</p>}
          {error && <div className="error-box">{error}</div>}
          {localOnly && <p className="muted">本地结果（该网盘不支持服务端搜索）。</p>}
          {results.map((r, i) => (
            <div
              key={r.ref}
              className={`palette-row${i === activeIdx ? ' active' : ''}`}
              onClick={() => {
                onPick(activeMount!, r.ref)
                requestClose()
              }}
              onMouseEnter={() => setActiveIdx(i)}
            >
              <span className="file-icon" aria-hidden>
                <FileTypeIcon name={r.name} isDir={r.isDir} />
              </span>
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{highlight(r.name, query)}</span>
              <span className="muted">· {r.path}</span>
              <span className="muted" style={{ marginLeft: 'auto', flexShrink: 0, fontSize: 12 }}>
                {!r.isDir && `${formatSize(r.size)} · `}
                {formatTime(r.modTime)}
              </span>
            </div>
          ))}
          {activeMount && query.trim() && !searching && !error && results.length === 0 && <p className="muted">没有找到“{query.trim()}”。</p>}
        </div>
        <div className="muted" style={{ marginTop: 12, fontSize: 12 }}>
          ↑↓ 选择 · 回车 打开 · Esc 关闭
        </div>
      </div>
    </div>
  )
}
