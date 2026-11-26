// SPDX-License-Identifier: MIT
// UploadTargetDialog — 拖拽上传的目标文件夹选择框。
// 拖文件进窗口后先弹这个框，不立即上传：默认当前文件夹；框里
// 列出当前目录下的子文件夹（api.browse，只取 IsDir），可以点进
// 子文件夹、点面包屑返回，也可以新建文件夹（后端 Mkdir；S3 写
// 零字节占位对象）。目标显示完整路径（网盘名 + /路径），点
// “上传到这里”才开始传，取消则不传任何文件。
//
// 样式只用 Drime tokens（卡片 #F5F5F5、边框 #E5E5E5、品牌绿
// #56E16C），动效复用 .dialog（基础曲线 500ms 入场、300ms 关闭），
// 只动 transform/opacity。字重 400。
import { useCallback, useEffect, useState } from 'react'
import type { Mount, Node } from '../mygo'
import { api, friendlyError } from '../api/client'
import { Skeleton, useClosing } from './motion'
import { FileTypeIcon } from './icons'

interface Crumb {
  name: string
  ref: string
}

export default function UploadTargetDialog({
  mount,
  initialRef,
  fileCount,
  onConfirm,
  onCancel,
  title,
  confirmLabel,
  verb,
  mounts,
  onMountChange,
}: {
  mount: Mount
  initialRef: string
  fileCount: number
  onConfirm: (targetRef: string) => void
  onCancel: () => void
  // Copy flow reuses this picker with different wording; the
  // upload flow keeps the defaults.
  title?: string
  confirmLabel?: string
  // 动作动词：默认按 confirmLabel 推断（复制/上传）；移动
  // 对话框传 "移动"，文案才不说“将复制到”。
  verb?: string
  // Copy flow: let the user switch the target mount inside the
  // dialog (upload flow passes none and the mount is fixed).
  mounts?: Mount[]
  onMountChange?: (m: Mount) => void
}) {
  const [closing, requestClose] = useClosing(onCancel)
  const [crumbs, setCrumbs] = useState<Crumb[]>([])
  const [dirs, setDirs] = useState<Node[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [newName, setNewName] = useState('')
  const [mkdirBusy, setMkdirBusy] = useState(false)
  const [mkdirError, setMkdirError] = useState<string | null>(null)

  const ref = crumbs.length > 0 ? crumbs[crumbs.length - 1].ref : initialRef

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const nodes = await api.browse(mount.id, ref)
      setDirs(nodes.filter((n) => n.isDir))
    } catch (e) {
      setDirs([])
      setError(friendlyError(e))
    } finally {
      setLoading(false)
    }
  }, [mount.id, ref])

  useEffect(() => {
    void load()
  }, [load])

  // Switching the target mount (copy flow) resets the folder
  // navigation — the crumbs belong to the previous mount.
  useEffect(() => {
    setCrumbs([])
  }, [mount.id])

  // Esc 关闭。
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') requestClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [requestClose])

  const createFolder = async () => {
    const name = newName.trim().replace(/^\/+|\/+$/g, '')
    if (!name || name.includes('/')) {
      setMkdirError('文件夹名不能留空，也不能含 /。')
      return
    }
    const target = ref ? `${ref}/${name}` : name
    setMkdirBusy(true)
    setMkdirError(null)
    try {
      await api.mkdir(mount.id, target)
      setNewName('')
      setCrumbs([...crumbs, { name, ref: target }])
    } catch (e) {
      setMkdirError(friendlyError(e))
    } finally {
      setMkdirBusy(false)
    }
  }

  const fullPath = `${mount.displayName}${ref ? ` / ${ref}` : ''}`

  return (
    <div className={`dialog-backdrop${closing ? ' closing' : ''}`} onClick={requestClose}>
      {/* dialog-flex（v0.10.0）：标题固定、中间 .dialog-body 滚动、
          底栏静态——长列表滚动时底栏一直可见；短内容时底栏在正常
          流里，不会像以前的 sticky 底栏那样盖住上面那行字。 */}
      <div className={`dialog dialog-flex${closing ? ' closing' : ''}`} onClick={(e) => e.stopPropagation()}>
        <h1>{title ?? '上传到哪里？'}</h1>
        <div className="dialog-body">
        <p className="muted" style={{ margin: '0 0 12px' }}>
          {fileCount} 个文件将{verb ?? (confirmLabel ? '复制' : '上传')}到下面选中的文件夹，点“{confirmLabel ?? '上传到这里'}”才开始。
        </p>

        {mounts && mounts.length > 0 && (
          <div className="field">
            <label>目标网盘</label>
            <select
              className="select"
              value={mount.id}
              onChange={(e) => {
                const next = mounts.find((m) => m.id === e.target.value)
                if (next) onMountChange?.(next)
              }}
            >
              {mounts.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.displayName}
                </option>
              ))}
            </select>
          </div>
        )}

        <div className="notice-box" style={{ wordBreak: 'break-all' }}>
          目标：{fullPath}
        </div>

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
        </div>

        {error ? (
          <div className="error-box">
            {error}
            <div style={{ marginTop: 8 }}>
              <button className="btn btn-sm" onClick={() => void load()}>
                重试
              </button>
            </div>
          </div>
        ) : loading ? (
          <div aria-label="正在加载文件夹">
            <Skeleton height={36} />
            <div style={{ marginTop: 8 }}>
              <Skeleton height={36} />
            </div>
          </div>
        ) : dirs.length === 0 ? (
          <p className="muted">当前文件夹下没有子文件夹，文件会直接上传到这里；也可以在下面新建一个。</p>
        ) : (
          <div style={{ display: 'grid', gap: 4, marginBottom: 12 }}>
            {dirs.map((d) => (
              <button
                key={d.ref || d.path}
                className="pane-row"
                style={{ border: 'none', background: 'none', textAlign: 'left', width: '100%' }}
                onClick={() => setCrumbs([...crumbs, { name: d.name, ref: d.ref || d.path }])}
              >
                <span className="file-icon" aria-hidden>
                  <FileTypeIcon name={d.name} isDir />
                </span>
                {d.name}
              </button>
            ))}
          </div>
        )}

        <div className="field">
          <label>新建文件夹（在当前目标下创建，建好后自动进入）</label>
          <div style={{ display: 'flex', gap: 8 }}>
            <input
              className="input"
              placeholder="例如：照片"
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') void createFolder()
              }}
            />
            <button className="btn" disabled={mkdirBusy} onClick={() => void createFolder()}>
              {mkdirBusy ? '新建中…' : '新建'}
            </button>
          </div>
          {mkdirError && <div className="hint" style={{ color: 'var(--danger)' }}>{mkdirError}</div>}
        </div>
        </div>

        <div className="dialog-actions">
          <button className="btn" onClick={requestClose}>
            取消
          </button>
          <button className="btn-primary" onClick={() => onConfirm(ref)}>
            {confirmLabel ?? '上传到这里'}
          </button>
        </div>
      </div>
    </div>
  )
}
