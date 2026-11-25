// Sidebar — left navigation, kept on every page (the full-screen
// dual-pane must never hide it). Sections: pages, mounts grouped by
// Group, Agent summary card. Group name is followed directly by its
// count badge ("工作 2"); edit/delete only appear on hover, delete
// needs a second confirmation, and deleting a non-empty group forces
// a migration destination first — mounts are never deleted with a
// group.
//
// Mounts and their groups are persisted in SQLite (internal/cache):
// they survive a restart. Removing a mount deletes only the local
// record (mount row, cached listings, encrypted credential blob);
// cloud files are never touched.
import { useState } from 'react'
import type { AgentStatus, Mount } from '../mygo'
import { api, friendlyError } from '../api/client'
import { t, type Lang } from '../lib/i18n'
import { Skeleton, staggerStyle } from '../components/motion'
import { mountStatusLabel } from '../lib/format'

export type Page = 'browse' | 'sync' | 'settings'

const UNGROUPED = ''

export default function Sidebar({
  page,
  lang,
  loading,
  onNavigate,
  mounts,
  activeMountId,
  onSelectMount,
  agentStatus,
  onGroupsChanged,
  onAddMount,
  onRemoveMount,
}: {
  page: Page
  lang: Lang
  loading?: boolean
  onNavigate: (p: Page) => void
  mounts: Mount[]
  activeMountId: string | null
  onSelectMount: (m: Mount) => void
  agentStatus: AgentStatus | null
  onGroupsChanged: () => void
  onAddMount: () => void
  onRemoveMount: (id: string) => void
}) {
  const [extraGroups, setExtraGroups] = useState<string[]>([])
  const [newGroupName, setNewGroupName] = useState('')
  const [addingGroup, setAddingGroup] = useState(false)
  const [renaming, setRenaming] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [deleting, setDeleting] = useState<string | null>(null)
  const [removingMount, setRemovingMount] = useState<string | null>(null)
  const [removingBusy, setRemovingBusy] = useState(false)
  const [migrateTo, setMigrateTo] = useState<string>('__ungrouped__')
  const [newMigrateGroup, setNewMigrateGroup] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const groupsInUse = Array.from(new Set(mounts.map((m) => m.group ?? '')))
  const allGroups = Array.from(new Set([...groupsInUse, ...extraGroups]))
  const orderedGroups = [...allGroups.filter((g) => g !== UNGROUPED).sort(), ...(allGroups.includes(UNGROUPED) ? [UNGROUPED] : [])]
  const groupLabel = (g: string) => (g === UNGROUPED ? '未分组' : g)
  const mountsOf = (g: string) => mounts.filter((m) => (m.group ?? '') === g)

  const createGroup = () => {
    const name = newGroupName.trim()
    if (!name || allGroups.includes(name)) return
    setExtraGroups([...extraGroups, name])
    setNewGroupName('')
    setAddingGroup(false)
  }

  const commitRename = async (oldName: string) => {
    const name = renameValue.trim()
    if (!name || name === oldName) {
      setRenaming(null)
      return
    }
    setBusy(true)
    setError(null)
    try {
      for (const m of mountsOf(oldName)) await api.updateMountGroup(m.id, name)
      setExtraGroups(extraGroups.map((g) => (g === oldName ? name : g)))
      setRenaming(null)
      onGroupsChanged()
    } catch (e) {
      setError(friendlyError(e))
    } finally {
      setBusy(false)
    }
  }

  const commitDelete = async (g: string) => {
    const members = mountsOf(g)
    let dest = migrateTo
    if (members.length > 0) {
      if (dest === '__new__') {
        dest = newMigrateGroup.trim()
        if (!dest) return
        setExtraGroups([...extraGroups, dest])
      } else if (dest === '__ungrouped__') {
        dest = UNGROUPED
      }
    }
    setBusy(true)
    setError(null)
    try {
      for (const m of members) await api.updateMountGroup(m.id, dest)
      setExtraGroups(extraGroups.filter((x) => x !== g))
      setDeleting(null)
      onGroupsChanged()
    } catch (e) {
      setError(friendlyError(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <aside className="sidebar">
      <nav>
        {(
          [
            ['browse', '📁', t(lang, 'browse')],
            ['sync', '🔄', t(lang, 'sync')],
            ['settings', '⚙️', t(lang, 'settings')],
          ] as const
        ).map(([p, icon, label], i) => (
          <button
            key={p}
            className={`nav-item nav-enter${page === p ? ' active' : ''}`}
            style={staggerStyle(i)}
            onClick={() => onNavigate(p)}
            aria-current={page === p ? 'page' : undefined}
          >
            {icon} {label}
          </button>
        ))}
      </nav>

      <div className="nav-section">{t(lang, 'drives')}</div>
      {error && <div className="error-box">{error}</div>}
      {loading ? (
        <div style={{ display: 'grid', gap: 8, padding: '4px 0' }}>
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} height={56} radius={12} />
          ))}
        </div>
      ) : mounts.length === 0 ? (
        <div style={{ padding: '4px 4px 8px' }}>
          <p className="muted" style={{ fontSize: 13, margin: '0 0 8px' }}>
            还没有网盘，先添加一个才能浏览和同步。
          </p>
          <button className="btn-primary" style={{ width: '100%' }} onClick={onAddMount}>
            + 添加网盘
          </button>
        </div>
      ) : null}
      {orderedGroups.map((g) => {
        const members = mountsOf(g)
        const isDeleting = deleting === g
        return (
          <div key={g || '__ungrouped__'}>
            <div className="group-row">
              <span
                onClick={() => {
                  if (members[0]) onSelectMount(members[0])
                }}
                style={{ display: 'flex', alignItems: 'center', gap: 6, flex: 1, minWidth: 0 }}
              >
                {renaming === g ? (
                  <input
                    className="input"
                    style={{ padding: '2px 6px', flex: 1, minWidth: 0 }}
                    value={renameValue}
                    autoFocus
                    onChange={(e) => setRenameValue(e.target.value)}
                    onBlur={() => void commitRename(g)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') void commitRename(g)
                      if (e.key === 'Escape') setRenaming(null)
                    }}
                    onClick={(e) => e.stopPropagation()}
                  />
                ) : (
                  <>
                    <span className="group-name">{groupLabel(g)}</span>
                    <span className="badge">{members.length}</span>
                  </>
                )}
              </span>
              <span className="group-actions">
                <button
                  className="icon-btn"
                  title="改名"
                  onClick={() => {
                    setRenaming(g)
                    setRenameValue(g)
                  }}
                >
                  ✎
                </button>
                <button className="icon-btn" title="删除分组" onClick={() => setDeleting(g)}>
                  🗑
                </button>
              </span>
            </div>
            {isDeleting && (
              <div className="card" style={{ padding: 12, margin: '4px 0 8px 12px' }}>
                <p style={{ margin: '0 0 8px' }}>
                  <strong>删除分组“{groupLabel(g)}”？</strong>
                </p>
                {members.length === 0 ? (
                  <p className="muted" style={{ margin: '0 0 8px' }}>
                    里面没有网盘，直接删除。
                  </p>
                ) : (
                  <>
                    <p className="muted" style={{ margin: '0 0 8px' }}>
                      里面有 {members.length} 个网盘（{members.map((m) => m.displayName).join('、')}），先选一个去处，网盘本身不会被删除：
                    </p>
                    <select className="select" value={migrateTo} onChange={(e) => setMigrateTo(e.target.value)}>
                      <option value="__ungrouped__">移到“未分组”</option>
                      {orderedGroups
                        .filter((x) => x !== g && x !== UNGROUPED)
                        .map((x) => (
                          <option key={x} value={x}>
                            移到“{x}”
                          </option>
                        ))}
                      <option value="__new__">新建一个分组…</option>
                    </select>
                    {migrateTo === '__new__' && (
                      <input
                        className="input"
                        style={{ marginTop: 8 }}
                        placeholder="新分组名"
                        value={newMigrateGroup}
                        onChange={(e) => setNewMigrateGroup(e.target.value)}
                      />
                    )}
                  </>
                )}
                <div style={{ display: 'flex', gap: 8, marginTop: 12 }}>
                  <button
                    className="btn-danger"
                    disabled={busy || (members.length > 0 && migrateTo === '__new__' && !newMigrateGroup.trim())}
                    onClick={() => void commitDelete(g)}
                  >
                    确认删除
                  </button>
                  <button className="btn" onClick={() => setDeleting(null)}>
                    取消
                  </button>
                </div>
              </div>
            )}
            {members.map((m, mi) => {
              const st = mountStatusLabel(m.status)
              return (
                <div key={m.id}>
                  <div
                    className={`mount-card stagger-item${m.id === activeMountId ? ' active' : ''}`}
                    style={staggerStyle(mi)}
                    onClick={() => onSelectMount(m)}
                  >
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <div className="mount-name">{m.displayName}</div>
                      <div className="mount-meta">
                        {m.provider}
                        {st ? ` · ${st}` : ''}
                      </div>
                    </div>
                    <button
                      className="icon-btn"
                      title="移除网盘"
                      onClick={(e) => {
                        e.stopPropagation()
                        setRemovingMount(m.id)
                      }}
                    >
                      🗑
                    </button>
                  </div>
                  {removingMount === m.id && (
                    <div className="card" style={{ padding: 12, margin: '4px 0 8px 12px' }}>
                      <p style={{ margin: '0 0 8px' }}>
                        <strong>移除“{m.displayName}”？</strong>
                      </p>
                      <p className="muted" style={{ margin: '0 0 8px' }}>
                        只删本地记录和保存的凭证，云端 / 本地文件夹里的文件不动。
                      </p>
                      <div style={{ display: 'flex', gap: 8 }}>
                        <button
                          className="btn-danger"
                          disabled={removingBusy}
                          onClick={async () => {
                            setRemovingBusy(true)
                            setError(null)
                            try {
                              await api.removeMount(m.id)
                              setRemovingMount(null)
                              onRemoveMount(m.id)
                            } catch (e) {
                              setError(friendlyError(e))
                            } finally {
                              setRemovingBusy(false)
                            }
                          }}
                        >
                          确认移除
                        </button>
                        <button className="btn" onClick={() => setRemovingMount(null)}>
                          取消
                        </button>
                      </div>
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )
      })}

      {addingGroup ? (
        /* 新建分组改成上下两行：输入框独占一行，按钮在下一行。
           之前输入框和“确定”并排，输入框 width:100% 把按钮压到只剩
           30px 宽，“确定”两个字被迫竖排。 */
        <div style={{ marginTop: 4 }}>
          <input
            className="input"
            placeholder="分组名"
            value={newGroupName}
            autoFocus
            onChange={(e) => setNewGroupName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') createGroup()
              if (e.key === 'Escape') setAddingGroup(false)
            }}
          />
          <div style={{ display: 'flex', gap: 8, marginTop: 8 }}>
            <button className="btn-primary" onClick={createGroup}>
              确定
            </button>
            <button className="btn" onClick={() => setAddingGroup(false)}>
              取消
            </button>
          </div>
        </div>
      ) : (
        <button className="btn" style={{ marginTop: 4 }} onClick={() => setAddingGroup(true)}>
          + 新建分组
        </button>
      )}

      <div style={{ flex: 1 }} />

      <div className="card" style={{ padding: 12, cursor: 'pointer' }} onClick={() => onNavigate('settings')}>
        <div style={{ fontSize: 13, fontWeight: 550 }}>Agent</div>
        {agentStatus ? (
          <div className="muted" style={{ fontSize: 12, marginTop: 4 }}>
            {agentStatus.online}/{agentStatus.total} 在线
            {agentStatus.default ? ` · 默认：${agentStatus.default}` : ' · 未设默认'}
          </div>
        ) : (
          <div className="muted" style={{ fontSize: 12, marginTop: 4 }}>
            未配置 Agent
          </div>
        )}
        <div className="muted" style={{ fontSize: 12 }}>点此去设置 →</div>
      </div>
    </aside>
  )
}
