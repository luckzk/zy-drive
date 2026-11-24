// App — desktop shell skeleton (mygo Web frontend mode).
// Layout (borrowed from Pail's structure, rewritten for multi-cloud):
//   left: Mount list (one entry per account, not per vendor)
//   middle: file table (name / size / time, multi-select, breadcrumbs)
//   right: inspector (generic info on top, provider-specific "Advanced"
//          collapsed below, shown only when Caps says the feature exists)
//   bottom: transfer queue
//   top: command palette placeholder (Ctrl+K)
//
// This file is UI only: it calls the app layer through the api client
// (mygo.Bind in the desktop shell, HTTP in the dev shell). It never
// calls a cloud API directly and never contains vendor branches —
// differences come from the Mount's Caps.
//
// Sync-task UI (2026-11-24 decision): the sync wizard is dual-pane —
// left file manager = source, right file manager = target — because
// picking source and target side by side is easier to understand than
// two dropdowns. See pages/SyncJobs.tsx.
import { useState } from 'react'
import FileTable from './components/FileTable'
import Inspector from './components/Inspector'
import SyncWizard from './pages/SyncJobs'

type Mount = {
  id: string
  displayName: string
  provider: string
  status: string
  used?: string
}

const DEMO_MOUNTS: Mount[] = [
  { id: 'm-s3', displayName: '我的 R2', provider: 's3', status: 'ok', used: '12.4 GB / 100 GB' },
  { id: 'm-dropbox', displayName: 'Dropbox', provider: 'dropbox', status: 'ok', used: '3.1 GB / 2 TB' },
  { id: 'm-drive', displayName: '工作 Drive', provider: 'drive', status: 'auth_expired', used: '—' },
]

export default function App() {
  const [activeMount, setActiveMount] = useState(DEMO_MOUNTS[0])
  const [showSync, setShowSync] = useState(false)
  const [showPalette, setShowPalette] = useState(false)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100vh' }}>
      <header
        style={{ display: 'flex', gap: 12, alignItems: 'center', padding: '10px 16px', borderBottom: '1px solid var(--border)' }}
      >
        <h1 style={{ margin: 0 }}>ZyDrive</h1>
        <input
          placeholder="搜索全部网盘…  (Ctrl+K)"
          onFocus={() => setShowPalette(true)}
          style={{ flex: 1, padding: '8px 12px', border: '1px solid var(--border)', borderRadius: 8 }}
        />
        <button className="btn-primary" onClick={() => setShowSync(true)}>
          新建同步
        </button>
      </header>

      <div style={{ display: 'flex', flex: 1, minHeight: 0 }}>
        <aside style={{ width: 240, borderRight: '1px solid var(--border)', padding: 12, overflowY: 'auto' }}>
          <h2>网盘</h2>
          {DEMO_MOUNTS.map((m) => (
            <div
              key={m.id}
              onClick={() => setActiveMount(m)}
              className="card"
              style={{
                padding: 10,
                marginBottom: 8,
                cursor: 'pointer',
                borderColor: m.id === activeMount.id ? 'var(--brand)' : undefined,
              }}
            >
              <div>{m.displayName}</div>
              <div className="muted" style={{ fontSize: 12 }}>
                {m.provider} · {m.status === 'ok' ? m.used : '需要重新授权'}
              </div>
            </div>
          ))}
          <button style={{ width: '100%', marginTop: 8 }}>+ 添加网盘（三步向导）</button>
        </aside>

        <main style={{ flex: 1, padding: 16, overflowY: 'auto' }}>
          <div className="muted" style={{ marginBottom: 8 }}>
            {activeMount.displayName} / 照片 / 2026
          </div>
          <FileTable mountId={activeMount.id} />
        </main>

        <Inspector mountId={activeMount.id} />
      </div>

      <footer style={{ borderTop: '1px solid var(--border)', padding: '8px 16px' }}>
        <span className="muted">传输队列：暂无任务（V0.1 骨架，队列由 internal/transfer 实现）</span>
      </footer>

      {showPalette && (
        <div className="card" style={{ position: 'fixed', top: 64, left: '50%', transform: 'translateX(-50%)', width: 480, padding: 16 }}>
          <h2>命令面板</h2>
          <p className="muted">上传、下载、复制到…、新建同步任务（V0.1 骨架）</p>
          <button onClick={() => setShowPalette(false)}>关闭</button>
        </div>
      )}
      {showSync && <SyncWizard onClose={() => setShowSync(false)} mounts={DEMO_MOUNTS} />}
    </div>
  )
}
