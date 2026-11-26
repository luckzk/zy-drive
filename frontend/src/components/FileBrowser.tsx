// SPDX-License-Identifier: MIT
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
import { Skeleton, useClosing } from './motion'
import ContextMenu, { type ContextMenuEntry } from './ContextMenu'
import FileTable from './FileTable'
import Inspector from './Inspector'
import { revealLabel } from './PreviewPane'
import UploadTargetDialog from './UploadTargetDialog'
import QuotaBar from './QuotaBar'

interface Crumb {
  name: string
  ref: string
}

export default function FileBrowser({ mount, mounts, quota, onRefreshQuota }: { mount: Mount; mounts: Mount[]; quota?: MountQuota; onRefreshQuota?: () => void }) {
  const [crumbs, setCrumbs] = useState<Crumb[]>([])
  const [nodes, setNodes] = useState<Node[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<Node | null>(null)
  const [checked, setChecked] = useState<Set<string>>(new Set())
  // Batch actions (V0.9.0): download / copy for the checked
  // selection. Upload of local files stays drag-drop (no system
  // file picker exists); the batch bar says so.
  const [batchBusy, setBatchBusy] = useState(false)
  const [batchMsg, setBatchMsg] = useState<string | null>(null)
  const [batchError, setBatchError] = useState<string | null>(null)
  const [copyMount, setCopyMount] = useState<Mount | null>(null)
  const [copyRefs, setCopyRefs] = useState<string[]>([])
  // 右键菜单（v0.10.0）：菜单坐标 + 作用的目标（已选集合或单行）。
  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number; nodes: Node[] } | null>(null)
  const [moveMount, setMoveMount] = useState<Mount | null>(null)
  const [moveRefs, setMoveRefs] = useState<string[]>([])
  const [renameTarget, setRenameTarget] = useState<Node | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [renameError, setRenameError] = useState<string | null>(null)
  const [deleteTargets, setDeleteTargets] = useState<Node[] | null>(null)
  // “详情”菜单项打开 Inspector 时高级页默认展开：nonce 只在
  // 点“详情”时 +1，点别的行、手动收起后都不会误展开。
  const [inspectorOpenNonce, setInspectorOpenNonce] = useState(0)
  // 跨盘移动的收尾轮询（见 startMoveCleanupPoll）：与上传轮询
  // 各用一个计时器，卸载时都要清掉。
  const movePollTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [deleteClosing, requestDeleteClose, resetDeleteClose] = useClosing(() => setDeleteTargets(null))
  const [renameClosing, requestRenameClose, resetRenameClose] = useClosing(() => setRenameTarget(null))

  const checkedRefs = () => [...checked]

  // refs 显式传入：右键菜单可能只作用于单行（不在已选集合
  // 里），只清掉实际操作过的那些，不动其余已选项。
  const handleBatchDownload = async (refs: string[]) => {
    if (refs.length === 0 || batchBusy) return
    setBatchBusy(true)
    setBatchMsg(null)
    setBatchError(null)
    try {
      const settings = await api.getSettings()
      const dir = settings.downloadDir?.trim()
      if (!dir) {
        throw new Error('还没有设置默认下载文件夹：先去设置 → 通用里填一个，批量下载会下到那里。')
      }
      const res = await api.batchDownload(mount.id, refs, dir)
      if (!res) throw new Error('批量下载没有返回结果。')
      const skipped = res.skipped?.length ? `；${res.skipped.length} 个没下：${res.skipped.map((s) => `${s.path}（${s.reason}）`).join('、')}` : ''
      setBatchMsg(`已把 ${res.transfers?.length ?? 0} 个文件加入下载队列，进度见右下角传输胶囊${skipped}。`)
      setChecked((prev) => {
        const next = new Set(prev)
        refs.forEach((r) => next.delete(r))
        return next
      })
      void load()
    } catch (e) {
      setBatchError(friendlyError(e))
    } finally {
      setBatchBusy(false)
    }
  }

  const handleBatchCopyConfirm = async (targetRef: string) => {
    if (!copyMount || copyRefs.length === 0 || batchBusy) return
    const dst = copyMount
    const refs = copyRefs
    setCopyMount(null)
    setCopyRefs([])
    setBatchBusy(true)
    setBatchMsg(null)
    setBatchError(null)
    try {
      const res = await api.batchCopy(mount.id, refs, dst.id, targetRef)
      if (!res) throw new Error('批量复制没有返回结果。')
      const skipped = res.skipped?.length ? `；${res.skipped.length} 个没复制：${res.skipped.map((s) => `${s.path}（${s.reason}）`).join('、')}` : ''
      setBatchMsg(`已把 ${res.transfers?.length ?? 0} 个文件加入复制队列（${mount.displayName} → ${dst.displayName}），进度见右下角传输胶囊${skipped}。`)
      setChecked((prev) => {
        const next = new Set(prev)
        refs.forEach((r) => next.delete(r))
        return next
      })
    } catch (e) {
      setBatchError(friendlyError(e))
    } finally {
      setBatchBusy(false)
    }
  }

  // ---- 右键菜单（v0.10.0） ----
  // 右键某行：已在已选集合里 → 菜单作用于整个集合；否则只
  // 作用于该行（并选中它，Inspector 同步显示）。
  const handleContextMenu = (n: Node, e: React.MouseEvent) => {
    const key = nodeKey(n)
    let targets: Node[]
    if (checked.has(key)) {
      targets = [...checked]
        .map((k) => nodes.find((x) => nodeKey(x) === k))
        .filter((x): x is Node => !!x)
    } else {
      targets = [n]
      setSelected(n)
    }
    setCtxMenu({ x: e.clientX, y: e.clientY, nodes: targets })
  }

  // 菜单项按 Caps 显隐（domain/caps.go 的规则：不支持的能力
  // 不出现，而不是灰掉点不动）。9 个驱动 CanRename/CanDelete
  // 都是 true；DeleteToTrash 只在本地文件夹与 S3/R2 上为 false。
  const menuItems = (targets: Node[]): ContextMenuEntry[] => {
    const refs = targets.map(nodeKey)
    const single = targets.length === 1 ? targets[0] : null
    const items: ContextMenuEntry[] = []
    if (single) {
      items.push({
        label: '打开',
        onClick: () => {
          if (single.isDir) openDir(single)
          else setSelected(single)
        },
      })
      if (!single.isDir) items.push({ label: '预览', onClick: () => setSelected(single) })
    }
    items.push({ label: '下载', onClick: () => void handleBatchDownload(refs) })
    items.push({
      label: '复制到…',
      onClick: () => {
        setCopyRefs(refs)
        const others = mounts.filter((m) => m.id !== mount.id)
        if (others.length > 0) setCopyMount(others[0])
      },
    })
    items.push({
      label: '移动到…',
      onClick: () => {
        setMoveRefs(refs)
        // 默认本盘内移动（目标框里仍可换到别的盘）。
        setMoveMount(mount)
      },
    })
    if (single && mount.caps.canRename) {
      items.push({
        label: '重命名',
        onClick: () => {
          resetRenameClose()
          setRenameTarget(single)
          setRenameValue(single.name)
          setRenameError(null)
        },
      })
    }
    if (mount.caps.canDelete) {
      items.push({
        label: '删除',
        danger: true,
        onClick: () => {
          resetDeleteClose()
          setDeleteTargets(targets)
        },
      })
    }
    if (single && !single.isDir) {
      items.push('sep')
      items.push({ label: revealLabel(), onClick: () => void handleReveal(single) })
      items.push({ label: '用系统应用打开', onClick: () => void handleSystemOpen(single) })
    }
    if (single) {
      items.push('sep')
      items.push({
        label: '详情',
        onClick: () => {
          setSelected(single)
          setInspectorOpenNonce((n) => n + 1)
        },
      })
    }
    return items
  }

  const handleReveal = async (n: Node) => {
    setBatchMsg(null)
    setBatchError(null)
    try {
      await api.revealInFinder(mount.id, nodeKey(n))
      setBatchMsg(`已在文件管理器中打开 ${n.name} 所在位置。`)
    } catch (e) {
      setBatchError(friendlyError(e))
    }
  }

  const handleSystemOpen = async (n: Node) => {
    setBatchMsg(null)
    setBatchError(null)
    try {
      await api.openWithSystemApp(mount.id, nodeKey(n))
      setBatchMsg(`已用系统应用打开 ${n.name}。`)
    } catch (e) {
      setBatchError(friendlyError(e))
    }
  }

  const handleRenameConfirm = async () => {
    if (!renameTarget || batchBusy) return
    const name = renameValue.trim()
    if (!name || name.includes('/') || name.includes('\\')) {
      setRenameError('新名字不能为空，也不能含 /。')
      return
    }
    const target = renameTarget
    setBatchBusy(true)
    setBatchMsg(null)
    setBatchError(null)
    try {
      await api.renameNode(mount.id, nodeKey(target), name)
      setBatchMsg(`已重命名为 ${name}。`)
      requestRenameClose()
      setChecked((prev) => {
        const next = new Set(prev)
        next.delete(nodeKey(target))
        return next
      })
      if (selected && nodeKey(selected) === nodeKey(target)) setSelected(null)
      void load()
    } catch (e) {
      setRenameError(friendlyError(e))
    } finally {
      setBatchBusy(false)
    }
  }

  const handleDeleteConfirm = async () => {
    if (!deleteTargets || deleteTargets.length === 0 || batchBusy) return
    const targets = deleteTargets
    const refs = targets.map(nodeKey)
    setBatchBusy(true)
    setBatchMsg(null)
    setBatchError(null)
    try {
      const res = await api.deleteNodes(mount.id, refs)
      if (!res) throw new Error('删除没有返回结果。')
      const failed = res.failed?.length ? `；${res.failed.length} 个没删掉：${res.failed.map((s) => `${s.path}（${s.reason}）`).join('、')}` : ''
      setBatchMsg(`已删除 ${res.done?.length ?? 0} 个${failed}。`)
      requestDeleteClose()
      setChecked((prev) => {
        const next = new Set(prev)
        refs.forEach((r) => next.delete(r))
        return next
      })
      if (selected && refs.includes(nodeKey(selected))) setSelected(null)
      void load()
    } catch (e) {
      setBatchError(friendlyError(e))
    } finally {
      setBatchBusy(false)
    }
  }

  const handleMoveConfirm = async (targetRef: string) => {
    if (!moveMount || moveRefs.length === 0 || batchBusy) return
    const dst = moveMount
    const refs = moveRefs
    setMoveMount(null)
    setMoveRefs([])
    setBatchBusy(true)
    setBatchMsg(null)
    setBatchError(null)
    try {
      const res = await api.moveNodes(mount.id, refs, dst.id, targetRef)
      if (!res) throw new Error('移动没有返回结果。')
      const failed = res.failed?.length ? `；${res.failed.length} 个没移动：${res.failed.map((s) => `${s.path}（${s.reason}）`).join('、')}` : ''
      setChecked((prev) => {
        const next = new Set(prev)
        refs.forEach((r) => next.delete(r))
        return next
      })
      if (dst.id === mount.id) {
        // 同盘：Rename 已同步完成，刷新即可。
        setBatchMsg(`已移动 ${res.done?.length ?? 0} 个到 ${dst.displayName}${failed}。`)
        void load()
      } else {
        // 跨盘：文件在复制队列里跑，源文件由 worker 在各自复制
        // 成功后删除（复制失败的源文件绝不删）；现在不要 load()，
        // 列表里的源文件还真实存在，刷新后等收尾轮询处理。
        const queued = res.transfers?.length ?? 0
        setBatchMsg(
          `已把 ${queued} 个文件加入移动队列（${mount.displayName} → ${dst.displayName}），复制完成后自动删除源文件，进度见右下角传输胶囊${failed}。`,
        )
        startMoveCleanupPoll((res.transfers ?? []).filter((t): t is Transfer => !!t), refs)
      }
    } catch (e) {
      setBatchError(friendlyError(e))
    } finally {
      setBatchBusy(false)
    }
  }

  // 跨盘移动的收尾（v0.10.0）：文件由 worker 在复制成功后逐
  // 个删源；选中的文件夹本身还留着（空文件夹）。等队列里的
  // 移动全部终态后，文件夹下所有文件都已复制成功、且没有失败
  // 的，才删文件夹——有任何失败文件留在里面时绝不删。复用
  // 上传轮询的 2s 间隔与 30 分钟上限；队列里找不到的传输
  // 视为已终态（不阻塞轮询），但它的文件夹不参与清理——
  // 不知道它成功还是失败。
  const startMoveCleanupPoll = (transfers: Transfer[], refs: string[]) => {
    const ids = new Set(transfers.map((t) => t.id))
    if (ids.size === 0) {
      void load()
      return
    }
    const folderRefs = refs.filter((r) => nodes.find((n) => nodeKey(n) === r)?.isDir)
    const started = Date.now()
    const tick = async () => {
      let all: Transfer[]
      try {
        all = await api.transfers()
      } catch {
        movePollTimer.current = setTimeout(() => void tick(), 2000)
        return
      }
      const mine = all.filter((t) => ids.has(t.id))
      const terminal = mine.filter((t) => t.status === 'completed' || t.status === 'failed' || t.status === 'cancelled')
      const missing = [...ids].filter((id) => !mine.some((t) => t.id === id))
      if (terminal.length + missing.length < ids.size) {
        if (Date.now() - started > 30 * 60 * 1000) {
          void load()
          return
        }
        movePollTimer.current = setTimeout(() => void tick(), 2000)
        return
      }
      const completedRefs = new Set(mine.filter((t) => t.status === 'completed').map((t) => t.srcRef))
      const failedRefs = new Set(mine.filter((t) => t.status !== 'completed').map((t) => t.srcRef))
      const deletable: string[] = []
      for (const f of folderRefs) {
        const under = [...completedRefs, ...failedRefs].filter((r) => r.startsWith(f + '/'))
        const hasFailed = [...failedRefs].some((r) => r.startsWith(f + '/'))
        if (under.length > 0 && !hasFailed) deletable.push(f)
      }
      if (deletable.length > 0) {
        try {
          await api.deleteNodes(mount.id, deletable)
        } catch {
          // 空文件夹删不掉不报错：它不占数据，用户可手动删。
        }
      }
      void load()
    }
    movePollTimer.current = setTimeout(() => void tick(), 2000)
  }
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

  const [retrying, setRetrying] = useState(false)
  // 重试挂载本身（TestMount 按保存的凭证重建驱动，失败时
  // 返回真实原因），而不只是重新列目录：驱动是 nil 时重新
  // 列目录永远只会得到同一个错误。
  const retryMount = useCallback(async () => {
    setRetrying(true)
    try {
      await api.testMount(mount.id)
      await load()
    } catch (e) {
      setError(friendlyError(e))
    } finally {
      setRetrying(false)
    }
  }, [mount.id, load])

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

  // Unmount 时清掉两个轮询计时器（上传、跨盘移动收尾）。
  useEffect(
    () => () => {
      if (pollTimer.current) clearTimeout(pollTimer.current)
      if (movePollTimer.current) clearTimeout(movePollTimer.current)
    },
    [],
  )

  // Esc 关闭重命名/删除对话框（useClosing 的退出动画走完才
  // 真正关；与 UploadTargetDialog 各自的 Esc 监听一致）。
  useEffect(() => {
    if (!deleteTargets && !renameTarget) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      if (deleteTargets) requestDeleteClose()
      else requestRenameClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [deleteTargets, renameTarget, requestDeleteClose, requestRenameClose])

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
            {/* Drime 容量是工作区口径：数字前面标明是哪个工作区
                （个人空间 / 工作区 8762），不让用户误以为是账号总量。 */}
            {mount.workspaceLabel && <div className="quota-text" style={{ marginBottom: 2 }}>{mount.workspaceLabel}容量</div>}
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
              <button className="btn btn-sm" disabled={retrying} onClick={() => void retryMount()}>
                {retrying ? '重试中…' : '重试'}
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
            onContextMenu={handleContextMenu}
          />
        )}
        {checked.size > 0 && (
          <div className="notice-box" style={{ marginTop: 8 }}>
            <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
              <span style={{ fontSize: 13 }}>已选 {checked.size} 个</span>
              <button className="btn btn-sm" disabled={batchBusy} onClick={() => void handleBatchDownload(checkedRefs())}>
                {batchBusy ? '处理中…' : '下载到默认文件夹'}
              </button>
              <button
                className="btn btn-sm"
                disabled={batchBusy || mounts.filter((m) => m.id !== mount.id).length === 0}
                onClick={() => {
                  const others = mounts.filter((m) => m.id !== mount.id)
                  if (others.length > 0) {
                    setCopyRefs(checkedRefs())
                    setCopyMount(others[0])
                  }
                }}
              >
                复制到…
              </button>
              {mount.caps.canDelete && (
                <button
                  className="btn btn-sm btn-danger"
                  disabled={batchBusy}
                  onClick={() => {
                    resetDeleteClose()
                    setDeleteTargets([...checked].map((k) => nodes.find((x) => nodeKey(x) === k)).filter((x): x is Node => !!x))
                  }}
                >
                  删除
                </button>
              )}
              <button className="btn btn-sm" disabled={batchBusy} onClick={() => setChecked(new Set())}>
                取消选择
              </button>
              <span className="muted" style={{ fontSize: 12 }}>批量上传还是拖拽进窗口（选目标文件夹后上传）。</span>
            </div>
          </div>
        )}
        {/* The result lives OUTSIDE the selection bar on purpose:
            a successful download/copy clears the selection, and a
            message inside the bar would vanish with it. */}
        {batchMsg && <div className="notice-box" style={{ marginTop: 8, fontSize: 13 }}>{batchMsg}</div>}
        {batchError && <div className="error-box" style={{ marginTop: 8, fontSize: 13 }}>{batchError}</div>}
      </div>
      {selected && <Inspector node={selected} mount={mount} quota={quota} closing={inspectorClosing} onClose={closeInspector} openAdvancedNonce={inspectorOpenNonce} />}
      {copyMount && (
        <UploadTargetDialog
          mount={copyMount}
          mounts={mounts.filter((m) => m.id !== mount.id)}
          onMountChange={setCopyMount}
          initialRef={copyMount.rootRef}
          fileCount={copyRefs.length}
          title="复制到哪里？"
          confirmLabel="复制到这里"
          onConfirm={(targetRef) => void handleBatchCopyConfirm(targetRef)}
          onCancel={() => {
            setCopyMount(null)
            setCopyRefs([])
          }}
        />
      )}
      {moveMount && (
        <UploadTargetDialog
          mount={moveMount}
          mounts={mounts}
          onMountChange={setMoveMount}
          initialRef={moveMount.id === mount.id ? ref : moveMount.rootRef}
          fileCount={moveRefs.length}
          title="移动到哪里？"
          confirmLabel="移动到这里"
          verb="移动"
          onConfirm={(targetRef) => void handleMoveConfirm(targetRef)}
          onCancel={() => {
            setMoveMount(null)
            setMoveRefs([])
          }}
        />
      )}
      {renameTarget && (
        <div className={`dialog-backdrop${renameClosing ? ' closing' : ''}`} onClick={requestRenameClose}>
          <div className={`dialog${renameClosing ? ' closing' : ''}`} onClick={(e) => e.stopPropagation()}>
            <h1>重命名</h1>
            <input
              autoFocus
              value={renameValue}
              onChange={(e) => {
                setRenameValue(e.target.value)
                setRenameError(null)
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') void handleRenameConfirm()
              }}
              placeholder="新名字（只填文件名，不能含 /）"
              aria-label="新名字"
            />
            {renameError && <div className="error-box" style={{ marginTop: 8 }}>{renameError}</div>}
            <div className="dialog-actions">
              <button className="btn" onClick={requestRenameClose}>取消</button>
              <button className="btn-primary" disabled={batchBusy || renameValue.trim().length === 0} onClick={() => void handleRenameConfirm()}>
                {batchBusy ? '重命名中…' : '确定'}
              </button>
            </div>
          </div>
        </div>
      )}
      {deleteTargets && (
        <div className={`dialog-backdrop${deleteClosing ? ' closing' : ''}`} onClick={requestDeleteClose}>
          <div className={`dialog${deleteClosing ? ' closing' : ''}`} onClick={(e) => e.stopPropagation()}>
            <h1>{deleteTargets.length === 1 ? `删除“${deleteTargets[0].name}”？` : `删除 ${deleteTargets.length} 个文件？`}</h1>
            <p className="muted" style={{ margin: '0 0 8px', wordBreak: 'break-all' }}>
              {deleteTargets.slice(0, 5).map((n) => n.name).join('、')}
              {deleteTargets.length > 5 ? ` 等 ${deleteTargets.length} 个` : ''}
            </p>
            <p style={{ margin: '0 0 12px' }}>
              {mount.caps.deleteToTrash
                ? '将移入回收站，可在网盘回收站恢复。'
                : '将永久删除，无法恢复。'}
            </p>
            <div className="dialog-actions">
              <button className="btn" onClick={requestDeleteClose}>取消</button>
              <button className="btn-danger" disabled={batchBusy} onClick={() => void handleDeleteConfirm()}>
                {batchBusy ? '删除中…' : '删除'}
              </button>
            </div>
          </div>
        </div>
      )}
      {ctxMenu && <ContextMenu x={ctxMenu.x} y={ctxMenu.y} items={menuItems(ctxMenu.nodes)} onClose={() => setCtxMenu(null)} />}
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
