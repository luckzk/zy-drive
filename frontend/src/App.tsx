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
import type { Agent, AgentStatus, Mount, MountQuota, Settings, Transfer } from './mygo'
import { api, friendlyError } from './api/client'
import { t, type Lang } from './lib/i18n'
import { Skeleton } from './components/motion'
import Sidebar, { type Page } from './components/Sidebar'
import FileBrowser from './components/FileBrowser'
import TransferDock from './components/TransferDock'
import ToastHost, { type ToastItem } from './components/Toasts'
import CommandPalette from './components/CommandPalette'
import AddMountDialog from './components/AddMountDialog'
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
    </div>
  )
}
