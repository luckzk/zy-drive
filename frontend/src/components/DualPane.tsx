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
import { FileTypeIcon, IconCheck, OpenedFolderIcon } from './icons'

interface Crumb {
  name: string
  ref: string
}

function refOf(n: Node): string {
  return n.ref || n.path
}

// Split an initial value ref into the crumbs to browse to and the
// row to select: the last segment is the selected row, the rest are
// the crumbs to its parent. A root ref ("") selects nothing — the
// current folder itself is the root. Local refs are absolute
// ("local:///a/b"): the parent dir is browsed, the full path is
// the selected row.
function splitInitialRef(value: SideSel): { crumbs: Crumb[]; selected: string | null; localAbs: string } {
  if (value.mountId === 'local') {
    const p = value.ref.replace(/^local:\/\//, '') || '/'
    if (p === '/') return { crumbs: [], selected: null, localAbs: '/' }
    const slash = p.lastIndexOf('/')
    return { crumbs: [], selected: `local://${p}`, localAbs: slash <= 0 ? '/' : p.slice(0, slash) }
  }
  const ref = (value.ref || '').replace(/^\/+|\/+$/g, '')
  if (!ref) return { crumbs: [], selected: null, localAbs: '/' }
  const segs = ref.split('/')
  const crumbs: Crumb[] = []
  let cur = ''
  for (const s of segs.slice(0, -1)) {
    cur = cur ? `${cur}/${s}` : s
    crumbs.push({ name: s, ref: cur })
  }
  return { crumbs, selected: ref, localAbs: '/' }
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
  // Initial crumbs + selected row, recovered from the value (editing
  // an existing job re-opens its exact source/target path selected,
  // not the mount root). State is owned here after mount: the parent
  // only receives the effective selection via onChange.
  const [initial] = useState(() => splitInitialRef(value))
  const [crumbs, setCrumbs] = useState<Crumb[]>(initial.crumbs)
  // Selected row (full ref, file or folder), or null = the folder
  // currently browsed is the sync root. One pane's selection never
  // touches the other pane's.
  const [selectedRef, setSelectedRef] = useState<string | null>(initial.selected)
  const [localAbs, setLocalAbs] = useState<string>(isLocalInit ? initial.localAbs : '/')
  const [localInput, setLocalInput] = useState<string>(isLocalInit ? initial.localAbs : '/')
  const [nodes, setNodes] = useState<Node[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange

  const mount = endpoint === 'local' ? null : mounts.find((m) => m.id === endpoint) ?? null
  // The folder currently browsed: mount root / crumb ref / local abs path.
  const currentRef = endpoint === 'local' ? `local://${localAbs}` : crumbs.length > 0 ? crumbs[crumbs.length - 1].ref : mount?.rootRef ?? ''
  // The sync root is the selected row when one is picked, otherwise
  // the folder being browsed (the old "navigation is selection"
  // rule, now visible instead of implicit — the user always sees
  // which path the task will use).
  const effectiveRef = selectedRef ?? currentRef
  const selectedName = selectedRef ? selectedRef.replace(/\/+$/, '').split('/').pop() ?? '' : ''
  const browsedPath =
    endpoint === 'local'
      ? `本地 ${localAbs}`
      : `${mount?.displayName ?? ''}${crumbs.length > 0 ? ' / ' + crumbs.map((c) => c.name).join(' / ') : ''}`
  const displayPath =
    selectedRef == null
      ? browsedPath
      : endpoint === 'local'
        ? `本地 ${selectedRef.replace(/^local:\/\//, '')}`
        : `${browsedPath} / ${selectedName}`

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

  // Report the effective sync root whenever it changes: the
  // selected row, or the folder being browsed when nothing is
  // selected. The parent's task always uses exactly this ref.
  const reportedRef = useRef<string | null>(null)
  useEffect(() => {
    const sel: SideSel = { mountId: endpoint, ref: effectiveRef, label: displayPath }
    if (reportedRef.current !== `${sel.mountId}|${sel.ref}`) {
      reportedRef.current = `${sel.mountId}|${sel.ref}`
      onChangeRef.current(sel)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [endpoint, currentRef, selectedRef, displayPath])

  const pickEndpoint = (ep: string) => {
    setEndpoint(ep)
    setCrumbs([])
    setSelectedRef(null)
    if (ep === 'local') {
      setLocalAbs('/')
      setLocalInput('/')
    }
  }

  const goLocal = () => {
    const p = localInput.trim() || '/'
    setLocalAbs(p.startsWith('/') ? p : `/${p}`)
    setSelectedRef(null)
  }

  // Single click selects a row (file or folder) as the sync root.
  // Double-click on a folder still enters it (and clears the row
  // selection: the folder just entered becomes the root).
  const selectRow = (n: Node) => {
    if (endpoint === 'local') {
      const p = localAbs === '/' ? `/${n.name}` : `${localAbs}/${n.name}`
      setSelectedRef(`local://${p}`)
    } else {
      setSelectedRef(refOf(n))
    }
  }

  const enterFolder = (n: Node) => {
    if (!n.isDir) return
    setSelectedRef(null)
    if (endpoint === 'local') {
      setLocalAbs(localAbs === '/' ? `/${n.name}` : `${localAbs}/${n.name}`)
    } else {
      setCrumbs([...crumbs, { name: n.name, ref: refOf(n) }])
    }
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
            onClick={() => {
              setCrumbs([])
              setSelectedRef(null)
            }}
            disabled={crumbs.length === 0}
            style={{ fontWeight: 400 }}
          >
            {mount?.displayName ?? '根目录'}
          </button>
          {crumbs.map((c, i) => (
            <span key={`${c.ref}-${i}`} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              <span className="crumb-sep">/</span>
              <button
                onClick={() => {
                  setCrumbs(crumbs.slice(0, i + 1))
                  setSelectedRef(null)
                }}
              >
                {c.name}
              </button>
            </span>
          ))}
        </div>
      )}

      <p className="muted" style={{ fontSize: 12, margin: '8px 0 0', wordBreak: 'break-all' }}>
        已选同步根目录：{displayPath}
        {selectedRef == null && '（当前文件夹）'}
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
            <div
              className="pane-row"
              onClick={() => {
                setLocalAbs(parentAbs)
                setSelectedRef(null)
              }}
            >
              <span className="file-icon" aria-hidden>
                <OpenedFolderIcon />
              </span>
              ..
            </div>
          )}
          {nodes.length === 0 && <p className="muted">这个文件夹是空的。</p>}
          {nodes.map((n, i) => {
            const rowRef = endpoint === 'local' ? `local://${localAbs === '/' ? `/${n.name}` : `${localAbs}/${n.name}`}` : refOf(n)
            const selected = selectedRef === rowRef
            return (
              <div
                key={refOf(n)}
                className={`pane-row stagger-item${selected ? ' selected' : ''}`}
                style={staggerStyle(i)}
                role="option"
                aria-selected={selected}
                title={n.isDir ? '单击选中这个文件夹作为同步根目录，双击进入' : '单击选中这个文件作为同步根目录'}
                onClick={() => selectRow(n)}
                onDoubleClick={() => enterFolder(n)}
              >
                <span className="file-icon" aria-hidden>
                  <FileTypeIcon name={n.name} isDir={n.isDir} />
                </span>
                <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{n.name}</span>
                {selected && (
                  <span className="pane-check" aria-label="已选中">
                    <IconCheck />
                  </span>
                )}
                {!n.isDir && <span className="muted" style={{ fontSize: 12 }}>{formatSize(n.size)}</span>}
                {n.isDir && <span className="muted">›</span>}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
