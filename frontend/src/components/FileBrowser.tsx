// FileBrowser — the browse page. Breadcrumbs + table, full width by
// default: the Inspector only mounts after the user clicks a file or
// folder, and hides on blank click / re-click / close. Double-click
// enters a folder without opening the Inspector.
import { useCallback, useEffect, useState } from 'react'
import type { Mount, Node } from '../mygo'
import { api, friendlyError } from '../api/client'
import FileTable from './FileTable'
import Inspector from './Inspector'

interface Crumb {
  name: string
  ref: string
}

export default function FileBrowser({ mount }: { mount: Mount }) {
  const [crumbs, setCrumbs] = useState<Crumb[]>([])
  const [nodes, setNodes] = useState<Node[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<Node | null>(null)
  const [checked, setChecked] = useState<Set<string>>(new Set())

  const ref = crumbs.length > 0 ? crumbs[crumbs.length - 1].ref : mount.rootRef

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setNodes(await api.browse(mount.id, ref))
    } catch (e) {
      setNodes([])
      setError(friendlyError(e))
    } finally {
      setLoading(false)
    }
  }, [mount.id, ref])

  // Reset navigation when switching mounts.
  useEffect(() => {
    setCrumbs([])
    setSelected(null)
    setChecked(new Set())
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mount.id])

  useEffect(() => {
    void load()
  }, [load])

  const openDir = (n: Node) => {
    setCrumbs([...crumbs, { name: n.name, ref: n.ref || n.path }])
  }

  const nodeKey = (n: Node) => n.ref || n.path

  return (
    <div style={{ display: 'flex', flex: 1, minHeight: 0 }}>
      <div
        style={{ flex: 1, minWidth: 0, padding: 16, overflowY: 'auto' }}
        onClick={(e) => {
          // Blank click hides the Inspector.
          if (e.target === e.currentTarget) setSelected(null)
        }}
      >
        <div className="crumbs">
          <button onClick={() => setCrumbs([])} style={{ fontWeight: crumbs.length === 0 ? 550 : 400 }}>
            {mount.displayName}
          </button>
          {crumbs.map((c, i) => (
            <span key={`${c.ref}-${i}`} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              <span className="crumb-sep">/</span>
              <button onClick={() => setCrumbs(crumbs.slice(0, i + 1))}>{c.name}</button>
            </span>
          ))}
          <span style={{ flex: 1 }} />
          <button className="btn btn-sm" onClick={() => void load()}>
            刷新
          </button>
        </div>

        {error ? (
          <div className="error-box">
            {error}
            {mount.status === 'auth_expired' && <div style={{ marginTop: 8 }}>授权过期了，重新授权后就能继续浏览。</div>}
            <div style={{ marginTop: 8 }}>
              <button className="btn btn-sm" onClick={() => void load()}>
                重试
              </button>
            </div>
          </div>
        ) : loading ? (
          <p className="muted">加载中…</p>
        ) : (
          <FileTable
            nodes={nodes}
            selectedId={selected ? nodeKey(selected) : null}
            checked={checked}
            onSelect={setSelected}
            onToggleCheck={(n) => {
              setChecked((prev) => {
                const next = new Set(prev)
                const k = nodeKey(n)
                if (next.has(k)) next.delete(k)
                else next.add(k)
                return next
              })
            }}
            onOpen={openDir}
          />
        )}
        {checked.size > 0 && (
          <p className="muted" style={{ marginTop: 8 }}>
            已选 {checked.size} 个。批量上传 / 下载 / 复制尚未接入。
          </p>
        )}
      </div>
      {selected && <Inspector node={selected} mount={mount} onClose={() => setSelected(null)} />}
    </div>
  )
}
