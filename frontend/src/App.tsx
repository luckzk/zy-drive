// SPDX-License-Identifier: MIT
// App — desktop shell (mygo Web frontend mode), rebuilt from the
// user-confirmed UI Demo (v8, 2026-11-24). Layout:
//   top: ZyDrive + search (Ctrl+K) — sync creation lives on the
//         Sync Tasks page (v0.13.0: the header 新建同步 button was
//         removed; it duplicated the page's own 创建同步任务 button)
//   left: navigation + mounts grouped + Agent summary card (kept on
//         every page, including the sync dual-pane)
//   middle: file browsing (Inspector on demand) / sync jobs (scheme A
//           dual-pane) / settings (Agent list)
//   bottom-right: transfer capsule (no permanent bottom bar)
//
// All data comes from the backend through api/client (mygo.Bind).
// There is no demo data anywhere: no mounts means an empty state
// with an add-drive guide, never invented drives or files.
//
// Motion (ai-design-skills, B7): page switches fade + rise 500ms
// (base curve) via .page-enter, keyed by page; loading states are
// skeleton blocks shaped like the real content, never "加载中…" text.
import { useCallback, useEffect, useRef, useState } from 'react'
import type { Agent, AgentStatus, ExitState, FlushResult, Mount, MountQuota, Settings, Transfer, VolumeStatus } from './mygo'
import { event as mygoEvent } from 'mygo-runtime'
import { api, friendlyError } from './api/client'
import { t, type Lang } from './lib/i18n'
import { Skeleton } from './components/motion'
import Sidebar, { type Page } from './components/Sidebar'
import FileBrowser from './components/FileBrowser'
import TransferDock from './components/TransferDock'
import ToastHost, { type ToastItem } from './components/Toasts'
import CommandPalette from './components/CommandPalette'
import AddMountDialog from './components/AddMountDialog'
import ExitDialog from './components/ExitDialog'
import SyncJobsPage from './pages/SyncJobs'
import SettingsPage from './pages/Settings'

function applyTheme(theme: string) {
  document.documentElement.dataset.theme = theme === 'dark' ? 'dark' : 'light'
}

export default function App() {
  const [page, setPage] = useState<Page>('browse')
  const [mounts, setMounts] = useState<Mount[]>([])
  // Quotas: one batch call for all mounts, on open + every 5 min
  // (+ manual refresh from the browse header). Never per listing:
  // Drime must not be polled per List. A failed mount stays in the
  // map with quota.error set — the sidebar shows "容量暂不可用"
  // for that mount only, the rest still render.
  const [quotas, setQuotas] = useState<Record<string, MountQuota>>({})
  const [agents, setAgents] = useState<Agent[]>([])
  const [agentStatus, setAgentStatus] = useState<AgentStatus | null>(null)
  const [transfers, setTransfers] = useState<Transfer[]>([])
  // 已保存同步任务数（v0.15.0）：导航“同步任务”旁的角标
  // （Demo v8 .cnt）。只在 browse 页加载 + 回到 browse 页时
  // 重数——任务在同步页里增删，App 靠页面切换感知变化。
  const [syncJobCount, setSyncJobCount] = useState(0)
  const [activeMountId, setActiveMountId] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  // v0.18.0: async startup mount load — true while the backend
  // is still building mounts (the window is already open).
  const [mountsLoading, setMountsLoading] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [showPalette, setShowPalette] = useState(false)
  const [showAddMount, setShowAddMount] = useState(false)
  const [reauthMount, setReauthMount] = useState<Mount | null>(null)
  // Toasts (v0.13.0, top-right): transient confirmations such as
  // "已在文件管理器中打开 … 所在位置。" — at most three at once,
  // oldest dropped first.
  const [toasts, setToasts] = useState<ToastItem[]>([])
  const toastId = useRef(0)
  const pushToast = useCallback((message: string) => {
    const id = ++toastId.current
    setToasts((prev) => [...prev.slice(-2), { id, message }])
  }, [])
  const dismissToast = useCallback((id: number) => {
    setToasts((prev) => prev.filter((t) => t.id !== id))
  }, [])
  // v0.19.0：FUSE 卷状态（GetMountStatus），每 2 秒轮询，驱动
  // 侧栏挂载开关与状态行。某盘由非失败变为失败时发一个 Toast
  // （卡片上同时有红字人话原因）；轮询失败（开发壳里没后端）
  // 静默保留旧状态，侧栏按保存值显示开关。
  const [volumeStatus, setVolumeStatus] = useState<VolumeStatus | null>(null)
  const volumeStatusRef = useRef<VolumeStatus | null>(null)
  const loadVolumeStatus = useCallback(async () => {
    try {
      const vs = await api.getMountStatus()
      const prev = volumeStatusRef.current
      if (prev) {
        for (const e of vs.mounts ?? []) {
          const p = (prev.mounts ?? []).find((x) => x.mountId === e.mountId)
          if (p && p.state !== 'failed' && e.state === 'failed') {
            pushToast(`“${e.displayName}”挂载失败：${e.error ?? '原因未知'}，开关还开着，关掉再打开可以重试。`)
          }
        }
      }
      volumeStatusRef.current = vs
      setVolumeStatus(vs)
    } catch {
      // 没接上后端时不报错、不清旧状态。
    }
  }, [pushToast])
  useEffect(() => {
    void loadVolumeStatus()
    const t = setInterval(() => void loadVolumeStatus(), 2000)
    return () => clearInterval(t)
  }, [loadVolumeStatus])

  // v0.19.0：退出 dirty 对话框。触发源是 Go 侧的退出拦截
  // （mygo 事件 "zydrive:exit-request"，见 ExitDialog.tsx 头注；
  // 另有同名 DOM 事件与 window.__zydriveRequestExit 备用入口）。
  // 前端只负责问用户 + 刷盘；“退出”本身由 Go 侧执行：用户的
  // 选择经 __zydriveExitDecision(quit) 回调或
  // "zydrive:exit-decision" DOM 事件回传 Go，Go 再调 App.Quit()。
  // __zydriveExitDecision 的落点在下面那个 useEffect 里，接
  // 的是 api.confirmExit（ConfirmExit Bind）。
  const [exitDialog, setExitDialog] = useState<ExitState | null>(null)
  const [exitFlushing, setExitFlushing] = useState(false)
  const [exitFlush, setExitFlush] = useState<FlushResult | null>(null)
  const [exitFlushError, setExitFlushError] = useState<string | null>(null)
  const exitDialogOpenRef = useRef(false)
  const notifyExitDecision = useCallback((quit: boolean) => {
    try {
      const w = window as unknown as { __zydriveExitDecision?: (quit: boolean) => void }
      w.__zydriveExitDecision?.(quit)
    } catch {
      // 回调未安装（不在 mygo 窗口里）：决定发不出去。
    }
    window.dispatchEvent(new CustomEvent('zydrive:exit-decision', { detail: { quit } }))
  }, [])
  const closeExitDialog = useCallback(
    (quit: boolean) => {
      exitDialogOpenRef.current = false
      setExitDialog(null)
      setExitFlush(null)
      setExitFlushError(null)
      notifyExitDecision(quit)
    },
    [notifyExitDecision],
  )
  const requestExit = useCallback(async () => {
    if (exitDialogOpenRef.current) return
    let st: ExitState
    try {
      st = await api.getMountExitState()
    } catch {
      // 读不到退出状态（没接后端）：不拦退出，决定权交回 Go。
      closeExitDialog(true)
      return
    }
    if (st.dirtyCount === 0 && st.openFiles === 0) {
      closeExitDialog(true)
      return
    }
    exitDialogOpenRef.current = true
    setExitFlush(null)
    setExitFlushError(null)
    setExitDialog(st)
  }, [closeExitDialog])
  useEffect(() => {
    let off: (() => void) | undefined
    try {
      // mygo 事件：Go 侧 mygo.NewEvent("zydrive:exit-request")。
      off = mygoEvent<unknown>('zydrive:exit-request').on(() => void requestExit())
    } catch {
      // 不在 mygo 窗口里（开发壳 / 纯浏览器）：没有这个事件源。
    }
    const onDom = () => void requestExit()
    window.addEventListener('zydrive:exit-request', onDom)
    const w = window as unknown as { __zydriveRequestExit?: () => void; __zydriveExitDecision?: (quit: boolean) => void }
    w.__zydriveRequestExit = () => void requestExit()
    // 决定回传的落点：调 ConfirmExit Bind，Go 侧收到 quit=true
    // 才真的退出（quit=false 是留在应用里，什么都不做）。
    w.__zydriveExitDecision = (quit: boolean) => {
      void api.confirmExit(quit).catch(() => {
        // 不在 mygo 窗口里（开发壳 / 纯浏览器）：没有后端可回传，忽略。
      })
    }
    return () => {
      off?.()
      window.removeEventListener('zydrive:exit-request', onDom)
      if (w.__zydriveRequestExit) delete w.__zydriveRequestExit
      if (w.__zydriveExitDecision) delete w.__zydriveExitDecision
    }
  }, [requestExit])
  const handleExitWait = useCallback(async () => {
    // 都传完了再点“继续等待”就是留在应用里，不再刷一次。
    if (exitFlush && exitFlush.remaining === 0) {
      closeExitDialog(false)
      return
    }
    if (exitFlushing) return
    setExitFlushing(true)
    setExitFlushError(null)
    try {
      const r = await api.flushMountDirty()
      setExitFlush(r)
      // 刷新剩余数：如实告诉用户还剩多少。
      try {
        setExitDialog(await api.getMountExitState())
      } catch {
        // 保留刷盘前的数字，不编新数。
      }
    } catch (e) {
      setExitFlushError(friendlyError(e))
    } finally {
      setExitFlushing(false)
    }
  }, [exitFlush, exitFlushing, closeExitDialog])
  // Settings live here because theme/language apply app-wide the
  // moment they change (Settings page edits them through these
  // handlers, which save + apply immediately).
  const [settings, setSettings] = useState<Settings | null>(null)
  const [theme, setTheme] = useState('light')
  const [lang, setLang] = useState<Lang>('zh')

  const activeMountIdRef = useRef<string | null>(null)
  const setActiveMount = (id: string | null) => {
    activeMountIdRef.current = id
    setActiveMountId(id)
  }

  const loadMounts = useCallback(async () => {
    try {
      const ms = await api.listMounts()
      setMounts(ms)
      setLoadError(null)
      if (ms.length > 0 && !ms.some((m) => m.id === activeMountIdRef.current)) {
        setActiveMount(ms[0].id)
      }
    } catch (e) {
      setLoadError(friendlyError(e))
    }
  }, [])

  const loadAgents = useCallback(async () => {
    try {
      setAgents(await api.listAgents())
      setAgentStatus(await api.agentStatus())
    } catch {
      // Agent list is non-fatal for browsing; the Settings page
      // reports its own load error.
    }
  }, [])

  const loadTransfers = useCallback(async () => {
    try {
      setTransfers(await api.transfers())
    } catch {
      // Dock polling is best-effort; a failed poll keeps old data.
    }
  }, [])

  const loadSyncJobCount = useCallback(async () => {
    try {
      setSyncJobCount((await api.listSyncJobs()).length)
    } catch {
      // 角标是非致命信息：失败就留着旧数，不挡浏览。
    }
  }, [])

  const loadQuotas = useCallback(async () => {
    try {
      const qs = await api.mountQuotas()
      const next: Record<string, MountQuota> = {}
      for (const q of qs) next[q.mountId] = q
      setQuotas(next)
    } catch {
      // Quota failure never breaks the app: the sidebar simply
      // shows no quota line until the next refresh.
    }
  }, [])

  const loadSettings = useCallback(async () => {
    try {
      const s = await api.getSettings()
      setSettings(s)
      setTheme(s.theme)
      setLang(s.language === 'en' ? 'en' : 'zh')
      applyTheme(s.theme)
    } catch {
      // Dev shell / no backend yet: keep light + zh defaults.
    }
  }, [])

  useEffect(() => {
    setLoading(true)
    void Promise.all([loadMounts(), loadAgents(), loadTransfers(), loadSettings(), loadQuotas(), loadSyncJobCount()]).finally(() => setLoading(false))
  }, [loadMounts, loadAgents, loadTransfers, loadSettings, loadQuotas, loadSyncJobCount])

  // v0.18.0: the backend loads mounts asynchronously after the
  // window opens, so poll mountsLoading() every 2s and refresh
  // the list while it is true — mounts fill in as they arrive.
  // Timeout chain (not a resident interval): stops when loading
  // ends, after 120s, on error, or on unmount.
  const mountsPollTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(() => {
    let cancelled = false
    let elapsed = 0
    const tick = async () => {
      if (cancelled) return
      let stillLoading = false
      try {
        stillLoading = await api.mountsLoading()
      } catch {
        // Dev shell / no backend: no async load to wait for.
        setMountsLoading(false)
        return
      }
      if (cancelled) return
      if (!stillLoading) {
        setMountsLoading(false)
        return
      }
      setMountsLoading(true)
      await loadMounts()
      if (cancelled) return
      elapsed += 2000
      if (elapsed >= 120000) {
        setMountsLoading(false)
        return
      }
      mountsPollTimer.current = setTimeout(() => void tick(), 2000)
    }
    void tick()
    return () => {
      cancelled = true
      if (mountsPollTimer.current) clearTimeout(mountsPollTimer.current)
    }
  }, [loadMounts])

  // 回到文件浏览页时重数一次（任务在同步页里增删）。
  useEffect(() => {
    if (page === 'browse') void loadSyncJobCount()
  }, [page, loadSyncJobCount])

  // Poll the transfer queue while the app is open.
  useEffect(() => {
    const t = setInterval(() => void loadTransfers(), 5000)
    return () => clearInterval(t)
  }, [loadTransfers])

  // Refresh quotas every 5 minutes (and on manual refresh from the
  // browse header); never per file listing.
  useEffect(() => {
    const t = setInterval(() => void loadQuotas(), 5 * 60 * 1000)
    return () => clearInterval(t)
  }, [loadQuotas])

  // Ctrl+K opens the command palette.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setShowPalette((v) => !v)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const activeMount = mounts.find((m) => m.id === activeMountId) ?? null

  const selectMount = (m: Mount) => {
    setActiveMount(m.id)
    setPage('browse')
  }

  // Theme/language change from the Settings page: apply immediately
  // (whole app follows, no restart). The Settings page owns saving:
  // it persists the draft after calling these, so there is exactly
  // one save path and an unsaved draft can't be clobbered here.
  const changeTheme = (next: string) => {
    setTheme(next)
    applyTheme(next)
    setSettings((prev) => (prev ? { ...prev, theme: next } : prev))
  }

  const changeLanguage = (next: Lang) => {
    setLang(next)
    setSettings((prev) => (prev ? { ...prev, language: next } : prev))
  }

  const openReauth = (m: Mount) => {
    setReauthMount(m)
    setShowAddMount(true)
  }

  return (
    <div className="app-shell">
      <header className="app-header">
        <img className="app-logo" src="/logo.svg" alt="ZyDrive" />
        <h1 style={{ margin: 0 }}>ZyDrive</h1>
        <input
          className="input"
          style={{ flex: 1 }}
          placeholder={t(lang, 'searchPlaceholder')}
          onFocus={() => setShowPalette(true)}
          readOnly
        />
      </header>

      <div className="app-body">
        <Sidebar
          page={page}
          lang={lang}
          loading={loading}
          mountsLoading={mountsLoading}
          onNavigate={setPage}
          mounts={mounts}
          quotas={quotas}
          activeMountId={activeMountId}
          onSelectMount={selectMount}
          agentStatus={agentStatus}
          onGroupsChanged={() => void loadMounts()}
          onAddMount={() => setShowAddMount(true)}
          onRemoveMount={() => void loadMounts()}
          onRefreshQuota={() => void loadQuotas()}
          // v0.11.0：拖拽排序松手时先乐观更新本地顺序（Sidebar
          // 内部算好新数组直接 setMounts），再调后端确认。
          onMountsReordered={(next) => setMounts(next)}
          syncJobCount={syncJobCount}
          volumeStatus={volumeStatus}
          onToast={pushToast}
          onVolumeStatusRefresh={() => void loadVolumeStatus()}
        />

        <main style={{ flex: 1, minWidth: 0, minHeight: 0, display: 'flex', flexDirection: 'column' }}>
          <div key={page} className="page-enter" style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }}>
            {page === 'browse' && (
              <>
                {loading ? (
                  <div style={{ padding: 16 }}>
                    <Skeleton height={20} width={240} />
                    <div style={{ marginTop: 16, display: 'grid', gap: 8 }}>
                      {[0, 1, 2, 3, 4].map((i) => (
                        <Skeleton key={i} height={36} />
                      ))}
                    </div>
                  </div>
                ) : loadError ? (
                  <div style={{ padding: 16 }}>
                    <div className="error-box">{loadError}</div>
                    <button className="btn" onClick={() => void loadMounts()}>
                      {t(lang, 'retry')}
                    </button>
                  </div>
                ) : mounts.length === 0 ? (
                  <div style={{ padding: 16 }}>
                    <div className="empty">
                      <h2>还没有添加网盘</h2>
                      <p>添加一个网盘后，就能在这里浏览、上传、下载，在同步任务里跨网盘复制。</p>
                      <button className="btn-primary" onClick={() => setShowAddMount(true)}>
                        添加网盘
                      </button>
                    </div>
                  </div>
                ) : activeMount ? (
                  <FileBrowser
                    key={activeMount.id}
                    mount={activeMount}
                    mounts={mounts}
                    onToast={pushToast}
                  />
                ) : null}
              </>
            )}
            {page === 'sync' && (
              <div style={{ flex: 1, overflowY: 'auto', padding: 16 }}>
                <SyncJobsPage mounts={mounts} agents={agents} onRan={() => void loadTransfers()} />
              </div>
            )}
            {page === 'settings' && (
              <div style={{ flex: 1, overflowY: 'auto', padding: 16 }}>
                <SettingsPage
                  lang={lang}
                  theme={theme}
                  quotas={quotas}
                  onThemeChange={changeTheme}
                  onLanguageChange={changeLanguage}
                  onReauth={openReauth}
                  onAddMount={() => setShowAddMount(true)}
                  onMountsChanged={() => void loadMounts()}
                />
              </div>
            )}
          </div>
        </main>
      </div>

      <TransferDock transfers={transfers} onRetry={() => void loadTransfers()} />

      <ToastHost toasts={toasts} onDismiss={dismissToast} />

      {showPalette && (
        <CommandPalette
          activeMount={activeMount}
          onClose={() => setShowPalette(false)}
          onPick={(m) => selectMount(m)}
        />
      )}
      {showAddMount && (
        <AddMountDialog
          initialProvider={reauthMount?.provider}
          initialStep={reauthMount ? 2 : 1}
          initialName={reauthMount?.displayName}
          initialGroup={reauthMount?.group ?? ''}
          reauthMount={reauthMount ?? undefined}
          onClose={() => {
            setShowAddMount(false)
            setReauthMount(null)
          }}
          onAdded={(m) => {
            setShowAddMount(false)
            setReauthMount(null)
            void loadMounts().then(() => setActiveMount(m.id))
          }}
        />
      )}
      {exitDialog && (
        <ExitDialog
          state={exitDialog}
          flushing={exitFlushing}
          flush={exitFlush}
          flushError={exitFlushError}
          onWait={() => void handleExitWait()}
          onQuitNow={() => closeExitDialog(true)}
          onStay={() => closeExitDialog(false)}
        />
      )}
    </div>
  )
}
