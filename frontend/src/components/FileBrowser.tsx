// FileBrowser — the browse page. Breadcrumbs + table, full width by
// default: the Inspector only mounts after the user clicks a file or
// folder, and hides on blank click / re-click / close. Double-click
// enters a folder without opening the Inspector.
//
// Drag-drop upload: the whole browse area is a drop zone. Real paths
// come from mygo-runtime onFileDrop (DOM File objects carry no path);
// the DOM drop handler only prevents the browser from navigating to
// the file, and in a plain browser it says honestly that drag upload
// needs the ZyDrive desktop window. Highlight + text use only the
// Drime tokens in tokens.css (pale green #f0fdf4 = the .file-table
// selected-row background, var(--brand) dashed border); progress
// lives in the TransferDock capsule (brand-green bar), which polls
// transfers() every 5s.
import { useCallback, useEffect, useRef, useState } from 'react'
import { isMyGo, onFileDrop } from 'mygo-runtime'
import type { Mount, MountQuota, Node, Transfer } from '../mygo'
import { api, friendlyError, type UploadSkipped } from '../api/client'
import { Skeleton } from './motion'
import FileTable from './FileTable'
import Inspector from './Inspector'
import UploadTargetDialog from './UploadTargetDialog'
import QuotaBar from './QuotaBar'

interface Crumb {
  name: string
  ref: string
}

export default function FileBrowser({ mount, quota, onRefreshQuota }: { mount: Mount; quota?: MountQuota; onRefreshQuota?: () => void }) {
  const [crumbs, setCrumbs] = useState<Crumb[]>([])
  const [nodes, setNodes] = useState<Node[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<Node | null>(null)
  const [checked, setChecked] = useState<Set<string>>(new Set())
  // Inspector close animates out (300ms) before unmounting.
  const [inspectorClosing, setInspectorClosing] = useState(false)
  const inspectorTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(
    () => () => {
      if (inspectorTimer.current) clearTimeout(inspectorTimer.current)
    },
    [],
  )
  const closeInspector = () => {
    if (inspectorClosing || !selected) return
    setInspectorClosing(true)
    inspectorTimer.current = setTimeout(() => {
      setSelected(null)
      setInspectorClosing(false)
    }, 300)
  }

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

  // ---- Drag-drop upload ----
  const zoneRef = useRef<HTMLDivElement>(null)
  const dragCount = useRef(0)
  const pollTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [dragOver, setDragOver] = useState(false)
  const [uploadInfo, setUploadInfo] = useState<string | null>(null)
  const [uploadError, setUploadError] = useState<string | null>(null)
  const [skipped, setSkipped] = useState<UploadSkipped[]>([])
  const [failedPaths, setFailedPaths] = useState<string[]>([])
  // 拖拽进来后先不传：等用户在目标选择框里确认文件夹。lastTarget
  // 记着上次传到的文件夹，重试失败文件时还传到那里。
  const [pendingPaths, setPendingPaths] = useState<string[] | null>(null)
  const [lastTargetRef, setLastTargetRef] = useState<string | null>(null)

  useEffect(
    () => () => {
      if (pollTimer.current) clearTimeout(pollTimer.current)
    },
    [],
  )

  // Uploads one batch of local paths into the given directory ref
  // and polls transfers() until the batch is terminal, then refreshes
  // the listing. Skipped paths (too big, unreadable) are shown
  // inline; failed ones can be retried on their own.
  const handleUpload = useCallback(
    async (paths: string[], targetRef?: string) => {
      if (paths.length === 0) return
      const dest = targetRef ?? ref
      setLastTargetRef(dest)
      if (pollTimer.current) clearTimeout(pollTimer.current)
      setUploadError(null)
      setFailedPaths([])
      setSkipped([])
      setUploadInfo(`正在上传 ${paths.length} 个文件…进度见右下角传输胶囊。`)
      let res
      try {
        res = await api.uploadFiles(mount.id, dest, paths)
      } catch (e) {
        setUploadInfo(null)
        setUploadError(friendlyError(e))
        return
      }
      if (res.skipped.length > 0) setSkipped(res.skipped)
      const ids = new Set(res.transfers.map((t) => t.id))
      if (ids.size === 0) {
        setUploadInfo(null)
        return
      }
      const started = Date.now()
      const tick = async () => {
        let all: Transfer[]
        try {
          all = await api.transfers()
        } catch {
          // Best-effort poll: the App-level 5s poll still updates
          // the dock; retry this check shortly.
          pollTimer.current = setTimeout(() => void tick(), 2000)
          return
        }
        const mine = all.filter((t) => ids.has(t.id))
        const terminal = mine.filter(
          (t) => t.status === 'completed' || t.status === 'failed' || t.status === 'cancelled',
        )
        const failed = mine.filter((t) => t.status === 'failed')
        if (mine.length > 0 && terminal.length >= mine.length) {
          setUploadInfo(null)
          if (failed.length > 0) {
            setFailedPaths(failed.map((t) => t.srcRef))
            setUploadError(`${failed.length} 个文件上传失败，可以只重试失败的文件。`)
          }
          void load()
          return
        }
        if (Date.now() - started > 30 * 60 * 1000) {
          // Give up watching (very large batch); the dock still
          // shows the transfers, and the user can refresh manually.
          setUploadInfo(null)
          void load()
          return
        }
        pollTimer.current = setTimeout(() => void tick(), 2000)
      }
      pollTimer.current = setTimeout(() => void tick(), 2000)
    },
    [mount.id, ref, load],
  )

  // mygo gives real local paths via onFileDrop; register only inside
  // the desktop window (guarded by isMyGo, registration failure
  // never breaks the page). The drop is accepted only when it lands
  // inside this browser area. A drop only opens the target picker —
  // nothing uploads until the user confirms a folder there.
  useEffect(() => {
    if (!isMyGo()) return
    let unsubscribe: (() => void) | undefined
    try {
      unsubscribe = onFileDrop(({ paths, x, y }) => {
        const zone = zoneRef.current
        const el = document.elementFromPoint(x, y)
        if (!zone || !el || !zone.contains(el)) return
        if (paths.length > 0) setPendingPaths(paths)
      })
    } catch {
      unsubscribe = undefined
    }
    return () => {
      unsubscribe?.()
    }
  }, [])

  return (
    <div
      ref={zoneRef}
      style={{ display: 'flex', flex: 1, minHeight: 0, position: 'relative' }}
      onDragEnter={(e) => {
        e.preventDefault()
        dragCount.current += 1
        setDragOver(true)
      }}
      onDragOver={(e) => {
        e.preventDefault()
        e.dataTransfer.dropEffect = isMyGo() ? 'copy' : 'none'
      }}
      onDragLeave={() => {
        dragCount.current = Math.max(0, dragCount.current - 1)
        if (dragCount.current === 0) setDragOver(false)
      }}
      onDrop={(e) => {
        e.preventDefault()
        dragCount.current = 0
        setDragOver(false)
        if (!isMyGo()) {
          // Plain browser: DOM File objects carry no local path, so
          // say so instead of pretending the drop worked.
          setUploadError('拖拽上传仅在 ZyDrive 桌面窗口中可用。在浏览器里打开时，请用桌面窗口拖拽上传。')
          return
        }
        // In the desktop window the real paths arrive via onFileDrop;
        // nothing to do here besides stopping the navigation.
      }}
    >
      {/* Drop highlight: transform/opacity only, no layout change.
          Pale green #f0fdf4 + brand-green dashed border are the
          existing .file-table selected-row / brand tokens. */}
      <div
        aria-hidden
        style={{
          position: 'absolute',
          inset: 8,
          zIndex: 5,
          pointerEvents: 'none',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          background: '#f0fdf4',
          border: '2px dashed var(--brand)',
          borderRadius: 'var(--radius)',
          opacity: dragOver ? 1 : 0,
          transform: dragOver ? 'scale(1)' : 'scale(0.98)',
          transition:
            'opacity 500ms cubic-bezier(0.22,1,0.36,1), transform 500ms cubic-bezier(0.22,1,0.36,1)',
        }}
      >
        <span style={{ fontSize: 14, fontWeight: 400, lineHeight: '20px', color: 'var(--ink)' }}>
          {isMyGo() ? '松开以选择上传文件夹' : '拖拽上传仅在 ZyDrive 桌面窗口中可用'}
        </span>
      </div>
      <div
        style={{ flex: 1, minWidth: 0, padding: 16, overflowY: 'auto' }}
        onClick={(e) => {
          // Blank click hides the Inspector.
          if (e.target === e.currentTarget) closeInspector()
        }}
      >
        <div className="crumbs">
          <button onClick={() => setCrumbs([])} style={{ fontWeight: 400 }}>
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

        {/* Same quota as the sidebar card (one App-level batch fetch,
            not a second call): the number here and there never disagree. */}
        <div className="browse-quota" style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 8 }}>
          <div style={{ flex: 1, minWidth: 0 }}>
            <QuotaBar quota={quota} />
          </div>
          {quota && !quota.unlimited && !quota.error && (
            <button className="btn btn-sm" onClick={() => onRefreshQuota?.()}>
              刷新容量
            </button>
          )}
        </div>

        {uploadInfo && <div className="notice-box">{uploadInfo}</div>}
        {skipped.length > 0 && (
          <div className="error-box">
            以下文件没有上传：
            {skipped.map((s) => (
              <div key={s.path} style={{ wordBreak: 'break-all', marginTop: 4 }}>
                {s.path}：{s.reason}
              </div>
            ))}
          </div>
        )}
        {uploadError && (
          <div className="error-box">
            {uploadError}
            {failedPaths.length > 0 && (
              <div style={{ marginTop: 8 }}>
                <button className="btn btn-sm" onClick={() => void handleUpload(failedPaths, lastTargetRef ?? ref)}>
                  重试失败的 {failedPaths.length} 个文件
                </button>
              </div>
            )}
          </div>
        )}

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
          <div aria-label="正在加载文件列表">
            <div style={{ display: 'flex', gap: 8, marginBottom: 8 }}>
              <Skeleton height={12} width={120} />
              <Skeleton height={12} width={80} />
              <Skeleton height={12} width={100} />
            </div>
            <div style={{ display: 'grid', gap: 8 }}>
              {[0, 1, 2, 3, 4, 5].map((i) => (
                <div key={i} style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
                  <Skeleton height={16} width={16} />
                  <Skeleton height={14} width={`${55 - i * 5}%`} />
                  <span style={{ flex: 1 }} />
                  <Skeleton height={14} width={64} />
                </div>
              ))}
            </div>
          </div>
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
      {selected && <Inspector node={selected} mount={mount} quota={quota} closing={inspectorClosing} onClose={closeInspector} />}
      {pendingPaths && (
        <UploadTargetDialog
          mount={mount}
          initialRef={ref}
          fileCount={pendingPaths.length}
          onConfirm={(targetRef) => {
            setPendingPaths(null)
            void handleUpload(pendingPaths, targetRef)
          }}
          onCancel={() => setPendingPaths(null)}
        />
      )}
    </div>
  )
}
