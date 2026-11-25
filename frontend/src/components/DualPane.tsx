// DualPane — one side of the sync dual-pane picker (Air Explorer
// style). Each pane is a small file manager: pick an endpoint (a
// mount, or a local folder), navigate into folders, and the folder
// you are standing in IS the sync root — there is no separate "pick
// a folder" step, and navigation never resets the choice. Local
// folders use the 'local' pseudo-mount with 'local://<abs path>' refs.
import { useCallback, useEffect, useRef, useState } from 'react'
import type { Mount, Node } from '../mygo'
import { api, friendlyError, type SideSel } from '../api/client'
import { formatSize } from '../lib/format'
import { Skeleton, staggerStyle } from './motion'

interface Crumb {
  name: string
  ref: string
}

function refOf(n: Node): string {
  return n.ref || n.path
}

export default function DualPane({
  label,
  value,
  onChange,
  mounts,
}: {
  label: string
  value: SideSel
  onChange: (s: SideSel) => void
  mounts: Mount[]
}) {
  const isLocalInit = value.mountId === 'local'
  const [endpoint, setEndpoint] = useState<string>(isLocalInit ? 'local' : value.mountId)
  const [crumbs, setCrumbs] = useState<Crumb[]>([])
  const [localAbs, setLocalAbs] = useState<string>(
    isLocalInit ? value.ref.replace(/^local:\/\//, '') || '/' : '/',
 // eslint-disable-line
  )
  const [localInput, setLocalInput] = useState<string>(isLocalInit ? value.ref.replace(/^local:\/\//, '') || '/' : '/')
  const [nodes, setNodes] = useState<Node[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange

  const mount = endpoint === 'local' ? null : mounts.find((m) => m.id === endpoint) ?? null
  // The ref currently browsed: mount root / crumb ref / local abs path.
  const currentRef = endpoint === 'local' ? `local://${localAbs}` : crumbs.length > 0 ? crumbs[crumbs.length - 1].ref : mount?.rootRef ?? ''
  const displayPath =
    endpoint === 'local'
      ? `本地 ${localAbs}`
      : `${mount?.displayName ?? ''}${crumbs.length > 0 ? ' / ' + crumbs.map((c) => c.name).join(' / ') : ''}`

  const load = useCallback(async (mountId: string, ref: string) => {
    setLoading(true)
    setError(null)
    try {
      setNodes(await api.browse(mountId, ref))
    } catch (e) {
      setNodes([])
      setError(friendlyError(e))
    } finally {
      setLoading(false)
    }
  }, [])

  // Load whenever the browsed folder changes.
  useEffect(() => {
    void load(endpoint, currentRef)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [endpoint, currentRef, load])

  // Report the current folder as the sync root whenever it changes.
  const reportedRef = useRef<string | null>(null)
  useEffect(() => {
    const sel: SideSel = { mountId: endpoint, ref: currentRef, label: displayPath }
    if (reportedRef.current !== `${sel.mountId}|${sel.ref}`) {
      reportedRef.current = `${sel.mountId}|${sel.ref}`
      onChangeRef.current(sel)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [endpoint, currentRef, displayPath])

  const pickEndpoint = (ep: string) => {
    setEndpoint(ep)
    setCrumbs([])
    if (ep === 'local') {
      setLocalAbs('/')
      setLocalInput('/')
    }
  }

  const goLocal = () => {
    const p = localInput.trim() || '/'
    setLocalAbs(p.startsWith('/') ? p : `/${p}`)
  }

  const parentAbs = localAbs === '/' ? null : localAbs.slice(0, localAbs.lastIndexOf('/')) || '/'

  return (
    <div className="pane">
      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        <strong>{label}</strong>
        <select className="select" style={{ flex: 1 }} value={endpoint} onChange={(e) => pickEndpoint(e.target.value)}>
          {mounts.map((m) => (
            <option key={m.id} value={m.id}>
              {m.displayName}（{m.provider}）
            </option>
          ))}
          <option value="local">本地文件夹</option>
        </select>
      </div>

      {endpoint === 'local' ? (
        <div style={{ display: 'flex', gap: 6, marginTop: 8 }}>
          <input
            className="input"
            value={localInput}
            onChange={(e) => setLocalInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') goLocal()
            }}
            placeholder="/Users/你的名字/照片"
            aria-label="本地文件夹路径"
          />
          <button className="btn" onClick={goLocal}>
            前往
          </button>
        </div>
      ) : (
        <div className="crumbs" style={{ margin: '8px 0 0' }}>
          <button
            onClick={() => setCrumbs([])}
            disabled={crumbs.length === 0}
            style={{ fontWeight: crumbs.length === 0 ? 550 : 400 }}
          >
            {mount?.displayName ?? '根目录'}
          </button>
          {crumbs.map((c, i) => (
            <span key={`${c.ref}-${i}`} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              <span className="crumb-sep">/</span>
              <button onClick={() => setCrumbs(crumbs.slice(0, i + 1))}>{c.name}</button>
            </span>
          ))}
        </div>
      )}

      <p className="muted" style={{ fontSize: 12, margin: '8px 0 0', wordBreak: 'break-all' }}>
        同步根目录：{displayPath}
        {endpoint === 'local' && '（本地端只能在本机执行）'}
      </p>

      {error ? (
        <div className="error-box" style={{ marginTop: 8 }}>
          {error}
          <div style={{ marginTop: 8 }}>
            <button className="btn btn-sm" onClick={() => void load(endpoint, currentRef)}>
              重试
            </button>
          </div>
        </div>
      ) : loading ? (
        <div className="pane-list" aria-label="正在加载文件夹">
          {[0, 1, 2, 3].map((i) => (
            <div key={i} style={{ display: 'flex', gap: 8, alignItems: 'center', padding: '8px 0' }}>
              <Skeleton height={14} width={14} />
              <Skeleton height={14} width={`${60 - i * 8}%`} />
            </div>
          ))}
        </div>
      ) : (
        <div className="pane-list">
          {endpoint === 'local' && parentAbs !== null && (
            <div className="pane-row" onClick={() => setLocalAbs(parentAbs)}>
              📁 ..
            </div>
          )}
          {nodes.length === 0 && <p className="muted">这个文件夹是空的。</p>}
          {nodes.map((n, i) => (
            <div
              key={refOf(n)}
              className="pane-row stagger-item"
              style={staggerStyle(i)}
              onClick={() => {
                if (!n.isDir) return
                if (endpoint === 'local') {
                  setLocalAbs(localAbs === '/' ? `/${n.name}` : `${localAbs}/${n.name}`)
                } else {
                  setCrumbs([...crumbs, { name: n.name, ref: refOf(n) }])
                }
              }}
              onDoubleClick={() => {
                if (!n.isDir) return
                if (endpoint === 'local') {
                  setLocalAbs(localAbs === '/' ? `/${n.name}` : `${localAbs}/${n.name}`)
                } else {
                  setCrumbs([...crumbs, { name: n.name, ref: refOf(n) }])
                }
              }}
            >
              <span>{n.isDir ? '📁' : '📄'}</span>
              <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{n.name}</span>
              {!n.isDir && <span className="muted" style={{ fontSize: 12 }}>{formatSize(n.size)}</span>}
              {n.isDir && <span className="muted">›</span>}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
