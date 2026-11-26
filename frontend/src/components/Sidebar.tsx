// SPDX-License-Identifier: MIT
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
import { useEffect, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import type { AgentStatus, Mount, MountQuota } from '../mygo'
import { api, friendlyError } from '../api/client'
import { t, type Lang } from '../lib/i18n'
import { Skeleton, staggerStyle } from '../components/motion'
import { BrandLogo, IconPencil, IconTrash, NavIcon } from './icons'
import { mountStatusLabel } from '../lib/format'
import QuotaBar from './QuotaBar'

export type Page = 'browse' | 'sync' | 'settings'

const UNGROUPED = ''

export default function Sidebar({
  page,
  lang,
  loading,
  onNavigate,
  mounts,
  quotas,
  activeMountId,
  onSelectMount,
  agentStatus,
  onGroupsChanged,
  onAddMount,
  onRemoveMount,
  onMountsReordered,
}: {
  page: Page
  lang: Lang
  loading?: boolean
  onNavigate: (p: Page) => void
  mounts: Mount[]
  quotas?: Record<string, MountQuota>
  activeMountId: string | null
  onSelectMount: (m: Mount) => void
  agentStatus: AgentStatus | null
  onGroupsChanged: () => void
  onAddMount: () => void
  onRemoveMount: (id: string) => void
  // v0.11.0：拖拽排序松手后乐观更新顺序。App 把 setMounts
  // 直接传进来；后端确认后 Sidebar 仍调 onGroupsChanged 重拉。
  onMountsReordered?: (next: Mount[]) => void
}) {
  const [extraGroups, setExtraGroups] = useState<string[]>([])
  const [newGroupName, setNewGroupName] = useState('')
  const [addingGroup, setAddingGroup] = useState(false)
  // 网盘标题旁的 + 菜单：新建网盘 / 新建分组。点其他地方、按 Esc、
  // 或选完一项后收起。
  const [menuOpen, setMenuOpen] = useState(false)
  const drivesHeadRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    if (!menuOpen) return
    const onPointer = (e: MouseEvent) => {
      if (drivesHeadRef.current && !drivesHeadRef.current.contains(e.target as Node)) setMenuOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setMenuOpen(false)
    }
    document.addEventListener('mousedown', onPointer)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onPointer)
      document.removeEventListener('keydown', onKey)
    }
  }, [menuOpen])
  const [renaming, setRenaming] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [deleting, setDeleting] = useState<string | null>(null)
  const [removingMount, setRemovingMount] = useState<string | null>(null)
  const [removingBusy, setRemovingBusy] = useState(false)
  // 长按拖拽排序（v0.11.0，重写 v0.10.0 的换组拖拽）：按住卡片
  // 400ms 且移动 ≤6px 进入拖拽；拖拽中原卡片留在原位
  // （visibility: hidden 占位），一个 fixed 克隆跟随指针；其余
  // 卡片按插入点用 transform 平移让位（200ms 过渡，走
  // .mount-card 已有的 transition），插入点处画一条 2px 品牌绿
  // 插入线；松手时克隆先动画落进最终位置，再乐观更新顺序并调
  // MoveMount 提交。位置更新走 rAF 节流 + 直接写 DOM style，
  // 拖拽中不经过 React state——v0.10.0 每 mousemove 都 setState
  // 是“不丝滑”最可能的根因。原生 draggable 做不了长按启动，
  // 所以用指针事件自实现；克隆是 fixed 定位，侧栏滚动也不影响
  // 跟手，几何在滚动时重测。
  const LONG_PRESS_MS = 400
  const DRAG_TOLERANCE_PX = 6
  const CARD_GAP_PX = 6 // .mount-card 的 margin-bottom
  const DROP_ANIM_MS = 200
  const EDGE_SCROLL_PX = 48
  const EDGE_SCROLL_STEP = 12

  type DragTarget = { group: string; index: number; newFlat: number }
  type CardGeom = { id: string; group: string; top: number; bottom: number; mid: number; left: number; width: number }
  type GroupSpan = { group: string; top: number; bottom: number; headerBottom: number }
  type DragGeom = { cards: CardGeom[]; spans: GroupSpan[]; flatOrder: string[] }
  type DragInfo = {
    mountId: string
    origGroup: string
    origIndex: number
    startX: number
    startY: number
    pointerX: number
    pointerY: number
    grabDX: number
    grabDY: number
    cardEl: HTMLElement
    clone: HTMLElement | null
    dragging: boolean
    dropping: boolean
    timer: ReturnType<typeof setTimeout>
    raf: number | null
    geom: DragGeom
    target: DragTarget | null
    shifts: Map<string, number>
    cardHeight: number
    cardLeft: number
    cardWidth: number
    lastScrollTop: number
    cancel: () => void
  }

  const sidebarRef = useRef<HTMLElement | null>(null)
  const lineRef = useRef<HTMLDivElement | null>(null)
  const [dragMountId, setDragMountId] = useState<string | null>(null)
  const [pressMountId, setPressMountId] = useState<string | null>(null)
  const dragRef = useRef<DragInfo | null>(null)
  // 拖拽结束后的 click 会落在卡片上：拦截掉，否则刚拖完又
  // 触发一次选中。普通单击不经过这里，不受影响。
  const suppressClickRef = useRef(false)

  // 侧栏几何：卡片按渲染顺序、分组按渲染顺序。拖拽开始时量
  // 一次，滚动时重测（rect 是视口坐标，滚了就变）。
  const measureGeometry = (): DragGeom | null => {
    const aside = sidebarRef.current
    if (!aside) return null
    const groupOf = (id: string) => mounts.find((m) => m.id === id)?.group ?? ''
    const cards: CardGeom[] = [...aside.querySelectorAll<HTMLElement>('[data-mount-id]')].map((el) => {
      const r = el.getBoundingClientRect()
      const id = el.getAttribute('data-mount-id') ?? ''
      return { id, group: groupOf(id), top: r.top, bottom: r.bottom, mid: (r.top + r.bottom) / 2, left: r.left, width: r.width }
    })
    const spans: GroupSpan[] = orderedGroups.map((g) => {
      const header = aside.querySelector<HTMLElement>(`[data-group-header="${CSS.escape(g)}"]`)
      const hr = header?.getBoundingClientRect()
      const members = cards.filter((c) => c.group === g)
      return {
        group: g,
        top: hr ? hr.top : (members[0]?.top ?? 0),
        bottom: members.length > 0 ? members[members.length - 1].bottom : (hr ? hr.bottom + 12 : 0),
        headerBottom: hr ? hr.bottom : 0,
      }
    })
    return { cards, spans, flatOrder: cards.map((c) => c.id) }
  }

  // 指针 Y → 插入点：先定分组（Y 落在哪个分组的纵向范围内，
  // 上下越界时夹到首/末组），再数该组里中点在指针上方的卡片
  // （不含被拖的）得组内 index；newFlat 是扁平列表里插入点前
  // 不含被拖卡片的张数（后端 MoveMount 的 index 语义与之一致）。
  const computeTarget = (geom: DragGeom, pointerY: number, dragId: string): DragTarget => {
    const { cards, spans } = geom
    let ti = 0
    for (let i = 0; i < spans.length; i++) {
      if (pointerY >= spans[i].top) ti = i
      else break
    }
    const targetGroup = spans[ti]?.group ?? ''
    const inGroup = cards.filter((c) => c.group === targetGroup && c.id !== dragId)
    let index = 0
    for (const c of inGroup) if (c.mid < pointerY) index++
    let newFlat = index
    for (let i = 0; i < ti; i++) {
      newFlat += cards.filter((c) => c.group === spans[i].group && c.id !== dragId).length
    }
    return { group: targetGroup, index, newFlat }
  }

  // 按插入点让位：被拖卡片仍占着原位（hidden），所以
  // 下移时 (oldFlat, newFlat] 的卡片上移填原位，上移时
  // [newFlat, oldFlat) 的卡片下移让出插入点；其余不动。
  // 插入线画在插入缝隙中点（上下两张卡视觉边沿的中点）。
  const applyInsertion = (d: DragInfo, target: DragTarget) => {
    const { cards, flatOrder } = d.geom
    const oldFlat = flatOrder.indexOf(d.mountId)
    const h = d.cardHeight + CARD_GAP_PX
    const aside = sidebarRef.current
    if (aside) {
      for (let f = 0; f < cards.length; f++) {
        const c = cards[f]
        if (c.id === d.mountId) continue
        let shift = 0
        if (target.newFlat > oldFlat && f > oldFlat && f <= target.newFlat) shift = -h
        else if (target.newFlat < oldFlat && f >= target.newFlat && f < oldFlat) shift = h
        if ((d.shifts.get(c.id) ?? 0) !== shift) {
          d.shifts.set(c.id, shift)
          const el = aside.querySelector<HTMLElement>(`[data-mount-id="${CSS.escape(c.id)}"]`)
          if (el) el.style.transform = shift ? `translateY(${shift}px)` : ''
        }
      }
    }
    const line = lineRef.current
    if (line) {
      const inGroup = cards.filter((c) => c.group === target.group && c.id !== d.mountId)
      let y: number
      if (inGroup.length === 0) {
        const span = d.geom.spans.find((s) => s.group === target.group)
        y = (span?.headerBottom ?? 0) + 6
      } else if (target.index === 0) {
        const next = inGroup[0]
        y = next.top + (d.shifts.get(next.id) ?? 0) - CARD_GAP_PX / 2
      } else if (target.index >= inGroup.length) {
        const prev = inGroup[inGroup.length - 1]
        y = prev.bottom + (d.shifts.get(prev.id) ?? 0) + CARD_GAP_PX / 2
      } else {
        const prev = inGroup[target.index - 1]
        const next = inGroup[target.index]
        y = (prev.bottom + (d.shifts.get(prev.id) ?? 0) + next.top + (d.shifts.get(next.id) ?? 0)) / 2
      }
      line.style.top = `${y - 1}px`
      line.style.left = `${d.cardLeft}px`
      line.style.width = `${d.cardWidth}px`
    }
    d.target = target
  }

  const clearShifts = () => {
    const aside = sidebarRef.current
    if (!aside) return
    aside.querySelectorAll<HTMLElement>('[data-mount-id]').forEach((el) => {
      el.style.transform = ''
    })
  }

  // 乐观重排：按渲染顺序（分组顺序 × 组内顺序）把被拖卡片
  // 插到 (group, index) 处。返回的新数组直接交给 App 的
  // setMounts；后端确认后 onGroupsChanged 再从服务器重拉。
  const reorderMounts = (mountId: string, group: string, index: number): Mount[] => {
    const moving = mounts.find((m) => m.id === mountId)
    if (!moving) return mounts
    const renderOrder = orderedGroups.flatMap((g) => mountsOf(g))
    const rest = renderOrder.filter((m) => m.id !== mountId)
    let flat = 0
    for (const g of orderedGroups) {
      const members = rest.filter((m) => (m.group ?? '') === g)
      if (g === group) {
        flat += Math.min(index, members.length)
        break
      }
      flat += members.length
    }
    const next = [...rest]
    next.splice(flat, 0, { ...moving, group })
    return next
  }

  const frame = () => {
    const d = dragRef.current
    if (!d || !d.dragging || d.dropping || !d.clone) return
    const aside = sidebarRef.current
    if (aside) {
      const r = aside.getBoundingClientRect()
      if (d.pointerY < r.top + EDGE_SCROLL_PX && aside.scrollTop > 0) aside.scrollTop -= EDGE_SCROLL_STEP
      else if (d.pointerY > r.bottom - EDGE_SCROLL_PX) aside.scrollTop += EDGE_SCROLL_STEP
      if (aside.scrollTop !== d.lastScrollTop) {
        d.lastScrollTop = aside.scrollTop
        const geom = measureGeometry()
        if (geom) d.geom = geom
      }
    }
    d.clone.style.transform = `translate(${d.pointerX - d.grabDX}px, ${d.pointerY - d.grabDY}px) scale(1.03)`
    const target = computeTarget(d.geom, d.pointerY, d.mountId)
    const prev = d.target
    if (!prev || prev.group !== target.group || prev.index !== target.index || prev.newFlat !== target.newFlat) {
      applyInsertion(d, target)
    }
  }

  // 拖拽期间 rAF 循环自续：只靠 pointermove 驱动的话，指针
  // 停在边缘等自动滚动时就没帧可跑、滚动会卡住。循环在松手
  // / 取消 / 落点动画开始时停（endTracking 取消待处理帧，
  // finishDrop 置 dropping 后 frame 自停）。
  const loop = () => {
    const d = dragRef.current
    if (d) d.raf = null
    frame()
    const dd = dragRef.current
    if (dd && dd.dragging && !dd.dropping) {
      dd.raf = requestAnimationFrame(loop)
    }
  }

  const scheduleFrame = () => {
    const d = dragRef.current
    if (!d || d.raf !== null) return
    d.raf = requestAnimationFrame(loop)
  }

  // 松手/取消：克隆先动画落进最终位置（无变化时回原位），
  // 再提交——先让 React 同步提交新顺序（flushSync），同一
  // 任务内清掉拖拽的全部内联 transform，中间没有一帧会画出
  // “旧顺序 + 已清 transform”的闪跳。失败时重拉回滚。
  const finishDrop = (d: DragInfo, cancelled: boolean) => {
    const target = cancelled ? null : d.target
    const noOp = !target || (target.group === d.origGroup && target.index === d.origIndex)
    let finalY = d.geom.cards.find((c) => c.id === d.mountId)?.top ?? 0
    if (!noOp && target) {
      const reduced = d.geom.cards.filter((c) => c.id !== d.mountId)
      if (target.newFlat < reduced.length) finalY = reduced[target.newFlat].top
      else if (reduced.length > 0) finalY = reduced[reduced.length - 1].bottom + CARD_GAP_PX
      else {
        const span = d.geom.spans.find((s) => s.group === target.group)
        finalY = (span?.headerBottom ?? finalY) + 6
      }
    }
    d.dropping = true
    const clone = d.clone
    if (clone) {
      clone.style.transition = 'transform 200ms cubic-bezier(0.22,1,0.36,1)'
      clone.style.transform = `translate(${d.cardLeft}px, ${finalY}px) scale(1)`
    }
    setTimeout(() => {
      if (!noOp && target && onMountsReordered) {
        const next = reorderMounts(d.mountId, target.group, target.index)
        flushSync(() => onMountsReordered(next))
      }
      if (clone) clone.remove()
      d.cardEl.classList.remove('drag-source')
      clearShifts()
      setDragMountId(null)
      if (noOp || !target) return
      setBusy(true)
      setError(null)
      api.moveMount(d.mountId, target.group, target.index).then(() => {
        onGroupsChanged()
      }).catch((err) => {
        setError(friendlyError(err))
        onGroupsChanged() // 回滚到服务器状态
      }).finally(() => setBusy(false))
    }, DROP_ANIM_MS + 30)
  }

  const onMountPointerDown = (m: Mount) => (e: React.PointerEvent) => {
    if (e.button !== 0) return
    if ((e.target as HTMLElement).closest('.mount-remove')) return
    if (dragRef.current) return
    const startX = e.clientX
    const startY = e.clientY
    const cardEl = e.currentTarget as HTMLElement
    const origIndex = mountsOf(m.group ?? '').findIndex((x) => x.id === m.id)
    const endTracking = (): DragInfo | null => {
      const dd = dragRef.current
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      window.removeEventListener('pointercancel', onCancel)
      if (dd) {
        if (dd.timer) clearTimeout(dd.timer)
        if (dd.raf !== null) cancelAnimationFrame(dd.raf)
      }
      dragRef.current = null
      setPressMountId(null)
      return dd
    }
    const onMove = (ev: PointerEvent) => {
      const d = dragRef.current
      if (!d) return
      d.pointerX = ev.clientX
      d.pointerY = ev.clientY
      if (!d.dragging) {
        // 按住后先移动：这是点击/滚动，不是拖拽。
        if (Math.hypot(ev.clientX - startX, ev.clientY - startY) > DRAG_TOLERANCE_PX) endTracking()
        return
      }
      scheduleFrame()
    }
    const onUp = () => {
      const d = endTracking()
      if (!d || !d.dragging) return
      suppressClickRef.current = true
      finishDrop(d, false)
    }
    const onCancel = () => {
      const d = endTracking()
      if (!d || !d.dragging) return
      suppressClickRef.current = true
      finishDrop(d, true)
    }
    const cancel = () => {
      const d = endTracking()
      if (!d || !d.dragging) return
      suppressClickRef.current = true
      finishDrop(d, true)
    }
    setPressMountId(m.id)
    dragRef.current = {
      mountId: m.id,
      origGroup: m.group ?? '',
      origIndex,
      startX,
      startY,
      pointerX: startX,
      pointerY: startY,
      grabDX: 0,
      grabDY: 0,
      cardEl,
      clone: null,
      dragging: false,
      dropping: false,
      timer: setTimeout(() => {
        const d = dragRef.current
        if (!d) return
        const geom = measureGeometry()
        if (!geom) return
        const cardGeom = geom.cards.find((c) => c.id === m.id)
        if (!cardGeom) return
        d.geom = geom
        d.cardHeight = cardGeom.bottom - cardGeom.top
        d.cardLeft = cardGeom.left
        d.cardWidth = cardGeom.width
        d.grabDX = d.pointerX - cardGeom.left
        d.grabDY = d.pointerY - cardGeom.top
        d.lastScrollTop = sidebarRef.current?.scrollTop ?? 0
        const clone = cardEl.cloneNode(true) as HTMLElement
        clone.classList.add('dragging')
        clone.style.position = 'fixed'
        clone.style.left = '0px'
        clone.style.top = '0px'
        clone.style.width = `${cardGeom.width}px`
        clone.style.margin = '0'
        clone.style.transition = 'none'
        clone.style.transform = `translate(${d.pointerX - d.grabDX}px, ${d.pointerY - d.grabDY}px) scale(1.03)`
        clone.style.pointerEvents = 'none'
        clone.setAttribute('aria-hidden', 'true')
        document.body.appendChild(clone)
        d.clone = clone
        cardEl.classList.add('drag-source')
        d.dragging = true
        setPressMountId(null)
        setDragMountId(m.id)
        scheduleFrame()
      }, LONG_PRESS_MS),
      raf: null,
      geom: { cards: [], spans: [], flatOrder: [] },
      target: null,
      shifts: new Map(),
      cardHeight: 0,
      cardLeft: 0,
      cardWidth: 0,
      lastScrollTop: 0,
      cancel,
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    window.addEventListener('pointercancel', onCancel)
  }
  // Esc 取消拖拽（正在按住时）；卸载时清掉未触发的计时器与克隆。
  useEffect(() => {
    if (!dragMountId) return
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') dragRef.current?.cancel()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [dragMountId])
  useEffect(
    () => () => {
      const d = dragRef.current
      if (d) {
        if (d.timer) clearTimeout(d.timer)
        if (d.raf !== null) cancelAnimationFrame(d.raf)
        if (d.clone) d.clone.remove()
        d.cardEl.classList.remove('drag-source')
      }
      dragRef.current = null
    },
    [],
  )
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
    <aside ref={sidebarRef} className={`sidebar${dragMountId ? ' dragging' : ''}`}>
      <nav>
        {(['browse', 'sync', 'settings'] as const).map((p, i) => (
          <button
            key={p}
            className={`nav-item nav-enter${page === p ? ' active' : ''}`}
            style={staggerStyle(i)}
            onClick={() => onNavigate(p)}
            aria-current={page === p ? 'page' : undefined}
          >
            <NavIcon page={p} /> {t(lang, p)}
          </button>
        ))}
      </nav>

      <div className="drives-head" ref={drivesHeadRef}>
        <span className="nav-section" style={{ margin: 0, flex: 1 }}>
          {t(lang, 'drives')}
        </span>
        <button
          className="icon-btn"
          title="添加网盘或分组"
          aria-label="添加网盘或分组"
          aria-expanded={menuOpen}
          onClick={() => setMenuOpen((v) => !v)}
        >
          +
        </button>
        {menuOpen && (
          <div className="sidebar-menu" role="menu">
            <button
              role="menuitem"
              onClick={() => {
                setMenuOpen(false)
                onAddMount()
              }}
            >
              新建网盘
            </button>
            <button
              role="menuitem"
              onClick={() => {
                setMenuOpen(false)
                setAddingGroup(true)
              }}
            >
              新建分组
            </button>
          </div>
        )}
      </div>
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
            还没有网盘，点上面网盘旁边的 + 添加一个才能浏览和同步。
          </p>
        </div>
      ) : null}
      {/* 首屏加载时序（2026-11-25 修复）：骨架与网盘行绝不同时
          出现。loading 为真时只渲染上面的骨架块，分组列表与
          新建分组输入框都要等加载完成再渲染——之前分组列表
          在三元表达式之外无条件渲染，网盘先出来了、骨架还在
          上面转。 */}
      {!loading && orderedGroups.map((g) => {
        const members = mountsOf(g)
        const isDeleting = deleting === g
        return (
          <div
            key={g || '__ungrouped__'}
            data-group={g}
          >
            <div className="group-row" data-group-header={g}>
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
                  aria-label="改名"
                  onClick={() => {
                    setRenaming(g)
                    setRenameValue(g)
                  }}
                >
                  <IconPencil />
                </button>
                <button className="icon-btn" title="删除分组" aria-label="删除分组" onClick={() => setDeleting(g)}>
                  <IconTrash />
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
                    data-mount-id={m.id}
                    className={`mount-card stagger-item${m.id === activeMountId ? ' active' : ''}${pressMountId === m.id ? ' pressing' : ''}`}
                    style={staggerStyle(mi)}
                    onClick={() => {
                      if (suppressClickRef.current) {
                        suppressClickRef.current = false
                        return
                      }
                      onSelectMount(m)
                    }}
                    // 长按期间不弹浏览器原生菜单。
                    onContextMenu={(e) => e.preventDefault()}
                    onPointerDown={onMountPointerDown(m)}
                  >
                    <BrandLogo provider={m.provider} endpointHost={m.endpointHost} />
                    <div style={{ flex: 1, minWidth: 0, paddingRight: 28 }}>
                      <div className="mount-name" title={m.displayName}>
                        {m.displayName}
                      </div>
                      <div className="mount-meta">
                        {m.provider}
                        {m.workspaceLabel ? ` · ${m.workspaceLabel}` : ''}
                        {st ? ` · ${st}` : ''}
                        {!m.verified && (
                          <span className="badge badge-amber" style={{ marginLeft: 6 }}>
                            未验证
                          </span>
                        )}
                      </div>
                      <QuotaBar quota={quotas?.[m.id]} compact />
                    </div>
                    <button
                      className="icon-btn mount-remove"
                      title="移除网盘"
                      aria-label="移除网盘"
                      onClick={(e) => {
                        e.stopPropagation()
                        setRemovingMount(m.id)
                      }}
                    >
                      <IconTrash />
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
      {/* v0.11.0 插入线：拖拽中由 JS 直接写 style 定位（top/left/
          width），平时不渲染。fixed 定位，不随侧栏滚动走。 */}
      {dragMountId && <div ref={lineRef} className="insert-line" aria-hidden />}

      {/* 新建分组输入框：只在从 + 菜单选了“新建分组”后出现，
          仍是上下两行（输入框独占一行，按钮在下一行），绝不竖排。
          分组本身是 session-only 的 extraGroups（V0.1 既有行为），
          这里不改持久化。 */}
      {addingGroup && !loading && (
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
      )}

      <div style={{ flex: 1 }} />

      <div className="card" style={{ padding: 12, cursor: 'pointer' }} onClick={() => onNavigate('settings')}>
        <div style={{ fontSize: 13, fontWeight: 400 }}>Agent</div>
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
