// App — desktop shell (mygo Web frontend mode), rebuilt from the
// user-confirmed UI Demo (v8, 2026-11-24). Layout:
//   top: ZyDrive + search (Ctrl+K) + 新建同步
//   left: navigation + mounts grouped + Agent summary card (kept on
//         every page, including the sync dual-pane)
//   middle: file browsing (Inspector on demand) / sync jobs (scheme A
//           dual-pane) / settings (Agent list)
//   bottom-right: transfer capsule (no permanent bottom bar)
//
// All data comes from the backend through api/client (mygo.Bind).
// There is no demo data anywhere: no mounts means an empty state
// with an add-drive guide, never invented drives or files.
import { useCallback, useEffect, useRef, useState } from 'react'
import type { Agent, AgentStatus, Mount, Transfer } from './mygo'
import { api, friendlyError } from './api/client'
import Sidebar, { type Page } from './components/Sidebar'
import FileBrowser from './components/FileBrowser'
import TransferDock from './components/TransferDock'
import CommandPalette from './components/CommandPalette'
import AddMountDialog from './components/AddMountDialog'
import SyncJobsPage from './pages/SyncJobs'
import SettingsPage from './pages/Settings'

export default function App() {
  const [page, setPage] = useState<Page>('browse')
  const [mounts, setMounts] = useState<Mount[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [agentStatus, setAgentStatus] = useState<AgentStatus | null>(null)
  const [transfers, setTransfers] = useState<Transfer[]>([])
  const [activeMountId, setActiveMountId] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [showPalette, setShowPalette] = useState(false)
  const [showAddMount, setShowAddMount] = useState(false)

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

  useEffect(() => {
    setLoading(true)
    void Promise.all([loadMounts(), loadAgents(), loadTransfers()]).finally(() => setLoading(false))
  }, [loadMounts, loadAgents, loadTransfers])

  // Poll the transfer queue while the app is open.
  useEffect(() => {
    const t = setInterval(() => void loadTransfers(), 5000)
    return () => clearInterval(t)
  }, [loadTransfers])

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

  const goCreateSync = () => {
    setPage('sync')
  }

  return (
    <div className="app-shell">
      <header className="app-header">
        <h1 style={{ margin: 0 }}>ZyDrive</h1>
        <input
          className="input"
          style={{ flex: 1 }}
          placeholder="搜索全部网盘… (Ctrl+K)"
          onFocus={() => setShowPalette(true)}
          readOnly
        />
        <button className="btn-primary" onClick={goCreateSync}>
          新建同步
        </button>
      </header>

      <div className="app-body">
        <Sidebar
          page={page}
          onNavigate={setPage}
          mounts={mounts}
          activeMountId={activeMountId}
          onSelectMount={selectMount}
          agentStatus={agentStatus}
          onGroupsChanged={() => void loadMounts()}
          onAddMount={() => setShowAddMount(true)}
        />

        <main style={{ flex: 1, minWidth: 0, minHeight: 0, display: 'flex', flexDirection: 'column' }}>
          {page === 'browse' && (
            <>
              {loading ? (
                <p className="muted" style={{ padding: 16 }}>
                  加载中…
                </p>
              ) : loadError ? (
                <div style={{ padding: 16 }}>
                  <div className="error-box">{loadError}</div>
                  <button className="btn" onClick={() => void loadMounts()}>
                    重试
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
                <FileBrowser key={activeMount.id} mount={activeMount} />
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
              <SettingsPage />
            </div>
          )}
        </main>
      </div>

      <TransferDock transfers={transfers} />

      {showPalette && (
        <CommandPalette
          activeMount={activeMount}
          onClose={() => setShowPalette(false)}
          onPick={(m) => selectMount(m)}
        />
      )}
      {showAddMount && <AddMountDialog onClose={() => setShowAddMount(false)} />}
    </div>
  )
}
