// SPDX-License-Identifier: MIT
// MobileHome — the phone home screen: storage cards, first.
//
// Design reference: Adom Shafi's "Cloud Storage App" Dribbble shot
// (full-size original at ~/workspace/browser_downloads/
// sess-1810883329/...-original-16ddaec864e444fbf478d93837e438fa.png).
// Only the PATTERN is taken from it — pastel card, brand logo in a
// white circle, name + account line, used/total + plan line, thick
// circular progress ring with % in the centre, horizontal
// snap-scroll carousel, centred mint header, filter pills, black
// floating bottom nav. The drawing is ours: card backgrounds are
// each card's THEME colour at 10% alpha and rings the same theme
// colour full-strength — the theme is NOT the logo brand colour:
// the reference's Drive card is warm cream with an amber ring
// (#FFF9E9 / #F5B81C), not Drive blue (measured from the shot
// 2026-11-26; see docs/mobile-app.md). Name, used numbers and %
// are 700 — a deliberate, user-approved exception to the standing
// de-bold rule, scoped to .mhome only (2026-11-26). Hexes are
// re-derived from the repo's own icons.tsx brand map — never the
// shot's exact hexes, never traced artwork.
//
// Round 2 (2026-11-26): centred header (deeper mint, no refresh
// button — refresh lives in the grid menu sheet), grid + search
// icon row (NO bell: there is no notification data source),
// filter pills (全部/云盘/本地 — our cards are mounts, not files,
// so the labels honestly differ from the shot's All/Folder/Images),
// search-by-name, 查看全部 toggles carousel ↔ vertical list,
// bottom nav with four REAL destinations (网盘/同步/传输/设置,
// no counts anywhere — there is no cross-mount index to count
// with), neutral-grey secondary text (#8A8F98) instead of the
// green-tinted desktop token. Recent Upload is NOT built: there
// is no recents API and no persisted transfer history; see
// docs/mobile-app.md for what it needs.
//
// Data — no mock, ever: cards render from the SAME backend calls
// the desktop sidebar uses (api.listMounts + api.mountQuotas);
// sync/transfers/settings views reuse the same api.* calls the
// desktop pages use (listSyncJobs, transfers polled every 5s,
// getSettings/saveSettings + previewCacheInfo/clearPreviewCache).
// S3/R2 has no quota API: the backend reports Unlimited and the
// card says 不限量, never an invented number. A mount whose quota
// fetch failed shows 容量暂不可用 with the backend's reason.
//
// Scope (decided 2026-11-26): the phone does view/browse, download
// and upload ONLY — there is deliberately NO "添加网盘" entry point
// anywhere on this screen. Mounts arrive via the desktop's encrypted
// config export/import (assumed path, not implemented yet). Folder
// cards and the category grid from the shot are the next step, not
// this one. Card tap does nothing yet — the mobile browse view
// ("tap a card opens that mount") is the next step; a card with no
// destination must not pretend to be a button.
import { useCallback, useEffect, useState } from 'react'
import type { ComponentType } from 'react'
import { api, friendlyError } from '../api/client'
import type { Mount, MountQuota, PreviewCacheInfo, Settings, SyncJob, Transfer } from '../mygo'
import { BrandLogo } from '../components/icons'
import { formatSize, formatSpeed, formatTime, transferStatusLabel } from '../lib/format'

// Theme colours — one accent per provider, used for the card
// background (10% alpha) and the ring (full strength). The accent
// is the CARD's theme, not the logo brand colour: the reference
// shot's Drive card is warm cream + amber ring, so Drive's accent
// is yellow #F5B81C while its logo stays the multicolour triangle.
// Every other provider's accent is its brand colour.
function themeOf(m: Mount): { accent: string; label: string } {
  switch (m.provider) {
    case 'drive':
      return { accent: '#F5B81C', label: 'Google Drive' }
    case 'dropbox':
      return { accent: '#0061FF', label: 'Dropbox' }
    case 'onedrive':
      return { accent: '#0078D4', label: 'OneDrive' }
    case 'drime':
      return { accent: '#56E16C', label: 'Drime' }
    case 's3':
      return (m.endpointHost ?? '').includes('r2.cloudflarestorage.com')
        ? { accent: '#F38020', label: 'Cloudflare R2' }
        : { accent: '#569A31', label: 'Amazon S3' }
    case 'box':
      return { accent: '#0061D5', label: 'Box' }
    case 'mega':
      return { accent: '#D9272E', label: 'MEGA' }
    case 'pcloud':
      return { accent: '#17BED0', label: 'pCloud' }
    case 'local':
      return { accent: '#56E16C', label: '本地文件夹' }
    default:
      return { accent: '#56E16C', label: m.provider }
  }
}

// Compact "10/15 GB" per the reference: integers when whole, no
// spaces around the slash, unit once when both sides share it.
// Different units fall back to "512 MB / 15.0 GB".
function splitSize(bytes: number): { value: string; unit: string } {
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = bytes
  let u = 0
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024
    u++
  }
  const value = Number.isInteger(v) ? String(v) : v.toFixed(1).replace(/\.0$/, '')
  return { value, unit: units[u] }
}

// Subtitle under the mount name: provider label + the scope the
// numbers belong to. Mount has no email field (accountId is a
// secrets-table reference, never displayable), so the honest
// second line is the provider + workspace (Drime) or bucket (S3).
function subtitleOf(m: Mount, label: string): string {
  if (m.provider === 'drime' && m.workspaceLabel) return `${label} · ${m.workspaceLabel}`
  if (m.provider === 's3' && m.rootLabel) return `${label} · ${m.rootLabel}`
  return label
}

function DotsIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
      <circle cx="12" cy="5" r="1.8" />
      <circle cx="12" cy="12" r="1.8" />
      <circle cx="12" cy="19" r="1.8" />
    </svg>
  )
}

function GridIcon({ size = 22 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
      <rect x="4" y="4" width="7" height="7" rx="1.5" />
      <rect x="13" y="4" width="7" height="7" rx="1.5" />
      <rect x="4" y="13" width="7" height="7" rx="1.5" />
      <rect x="13" y="13" width="7" height="7" rx="1.5" />
    </svg>
  )
}

function SearchIcon({ size = 22 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
      <circle cx="11" cy="11" r="7" />
      <path d="M20.5 20.5L16 16" />
    </svg>
  )
}

function CloudIcon({ size = 15 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M17.5 18H7a4 4 0 1 1 .58-7.97A5.5 5.5 0 0 1 18 12.6 3.25 3.25 0 0 1 17.5 18z" />
    </svg>
  )
}

function FolderIcon({ size = 15 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7z" />
    </svg>
  )
}

function HomeIcon() {
  return (
    <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M4 11l8-7 8 7" />
      <path d="M6 9.5V20h12V9.5" />
    </svg>
  )
}

function SyncIcon() {
  return (
    <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M21 12a9 9 0 1 1-2.64-6.36" />
      <path d="M21 3v6h-6" />
    </svg>
  )
}

function TransferIcon() {
  return (
    <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12 4v10" />
      <path d="M8 10l4 4 4-4" />
      <path d="M4 20h16" />
    </svg>
  )
}

function SlidersIcon() {
  return (
    <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
      <path d="M4 7h16" />
      <circle cx="9" cy="7" r="2" />
      <path d="M4 17h16" />
      <circle cx="15" cy="17" r="2" />
    </svg>
  )
}

// Ring — thick SVG progress ring with the % in the centre, per the
// shot. pct is clamped 0..100 (used can exceed total on a disk).
// Track is white (the reference's unfilled arc), not grey.
function Ring({ pct, color }: { pct: number; color: string }) {
  const size = 88
  const stroke = 10
  const r = (size - stroke) / 2
  const c = 2 * Math.PI * r
  const p = Math.max(0, Math.min(100, pct))
  return (
    <div className="mhome-ring" aria-label={`已用 ${Math.round(p)}%`}>
      <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} aria-hidden="true">
        <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke="#ffffff" strokeWidth={stroke} />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={r}
          fill="none"
          stroke={color}
          strokeWidth={stroke}
          strokeLinecap="round"
          strokeDasharray={c}
          strokeDashoffset={c * (1 - p / 100)}
          transform={`rotate(-90 ${size / 2} ${size / 2})`}
        />
      </svg>
      <span className="mhome-ring-pct">{Math.round(p)}%</span>
    </div>
  )
}

// UnlimitedSlot — the same 88px slot as the ring, but a mount with
// no quota total has no percentage to show: a soft theme circle
// with ∞ (unlimited, not a number). Never a faked %.
function UnlimitedSlot({ color }: { color: string }) {
  return (
    <div className="mhome-ring" aria-label="不限量">
      <svg width="88" height="88" viewBox="0 0 88 88" aria-hidden="true">
        <circle cx="44" cy="44" r="39" fill="none" stroke={color} strokeOpacity="0.25" strokeWidth="10" />
      </svg>
      <span className="mhome-ring-pct" style={{ color }}>
        ∞
      </span>
    </div>
  )
}

function StorageCard({
  mount,
  quota,
  onMenu,
}: {
  mount: Mount
  quota: MountQuota | undefined
  onMenu: (m: Mount) => void
}) {
  const theme = themeOf(mount)
  const bg = mount.provider === 'local' ? 'var(--card)' : `${theme.accent}1A`
  const q = quota
  return (
    <article className="mhome-card" style={{ background: bg }} data-mount-id={mount.id}>
      <div className="mhome-card-top">
        <span className="mhome-logo-circle">
          <BrandLogo provider={mount.provider} endpointHost={mount.endpointHost} size={28} />
        </span>
        <button
          type="button"
          className="mhome-menu-btn"
          aria-label={`更多操作（${mount.displayName}）`}
          onClick={() => onMenu(mount)}
        >
          <DotsIcon />
        </button>
      </div>
      <h3 className="mhome-name" title={mount.displayName}>
        {mount.displayName}
      </h3>
      <p className="mhome-sub">{subtitleOf(mount, theme.label)}</p>
      {!q ? (
        <div className="mhome-bottom">
          <div>
            <div className="mhome-used">容量读取中…</div>
          </div>
        </div>
      ) : q.error ? (
        <div className="mhome-bottom mhome-bottom-error">
          <div className="mhome-used mhome-error-text">容量暂不可用</div>
          <div className="mhome-sub mhome-error-reason">{q.error}</div>
        </div>
      ) : q.unlimited || q.total <= 0 ? (
        <div className="mhome-bottom">
          <div>
            <div className="mhome-used">
              {q.total === -1 && q.used > 0 ? (
                <>
                  已用 {splitSize(q.used).value} <span className="mhome-used-unit">{splitSize(q.used).unit}</span>
                </>
              ) : (
                '不限量'
              )}
            </div>
            <div className="mhome-sub">{q.total === -1 ? '不限量' : mount.provider === 's3' && mount.rootLabel ? `桶 ${mount.rootLabel}` : '无容量上限'}</div>
          </div>
          <UnlimitedSlot color={theme.accent} />
        </div>
      ) : (
        <div className="mhome-bottom">
          <div>
            <div className="mhome-used">
              {(() => {
                const u = splitSize(q.used)
                const t = splitSize(q.total)
                return u.unit === t.unit ? (
                  <>
                    {u.value}/{t.value} <span className="mhome-used-unit">{u.unit}</span>
                  </>
                ) : (
                  <>
                    {u.value} {u.unit} / {t.value} {t.unit}
                  </>
                )
              })()}
            </div>
            <div className="mhome-sub">
              {q.isLocalDisk ? '本地磁盘 · ' : ''}剩余 {formatSize(Math.max(0, q.total - q.used))}
            </div>
          </div>
          <Ring pct={(q.used / q.total) * 100} color={theme.accent} />
        </div>
      )}
    </article>
  )
}

function basename(p: string): string {
  return p.split('/').filter(Boolean).pop() ?? p
}

function runOnLabel(runOn: string): string {
  if (runOn === 'agent') return 'Agent 执行'
  if (runOn === 'server') return '服务器执行'
  return '本机执行'
}

// Sync view — the same listSyncJobs the desktop Sync Tasks page
// uses. Creating jobs stays on desktop; the phone lists them and
// shows schedule / enabled / last run honestly.
function SyncView() {
  const [jobs, setJobs] = useState<SyncJob[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const loadJobs = useCallback(async () => {
    try {
      setJobs(await api.listSyncJobs())
      setError(null)
    } catch (e) {
      setError(friendlyError(e))
    }
  }, [])
  useEffect(() => {
    void loadJobs()
  }, [loadJobs])
  return (
    <div>
      <div className="mhome-view-header">
        <h2 className="mhome-view-title">同步任务</h2>
        <button type="button" className="mhome-view-refresh" onClick={() => void loadJobs()}>
          刷新
        </button>
      </div>
      {error ? (
        <div className="mhome-load-error" role="alert">
          <p>{error}</p>
          <button type="button" className="mhome-btn" onClick={() => void loadJobs()}>
            重试
          </button>
        </div>
      ) : jobs === null ? (
        <div className="mhome-rows" aria-busy="true">
          <div className="mhome-row-card mhome-skeleton" style={{ minHeight: 76 }} />
          <div className="mhome-row-card mhome-skeleton" style={{ minHeight: 76 }} />
        </div>
      ) : jobs.length === 0 ? (
        <div className="mhome-empty">
          <h2 className="mhome-empty-title">还没有同步任务</h2>
          <p className="mhome-empty-text">在桌面端创建同步任务，手机上查看运行状态。</p>
        </div>
      ) : (
        <div className="mhome-rows" role="list" aria-label="同步任务">
          {jobs.map((j) => (
            <div className="mhome-row-card" role="listitem" key={j.id}>
              <div className="mhome-row-title">{j.name}</div>
              <div className="mhome-row-sub">
                {j.schedule ? `定时 ${j.schedule}` : '仅手动'} · {runOnLabel(j.runOn)} · {j.enabled ? '已启用' : '已停用'}
              </div>
              <div className="mhome-row-sub">
                {j.lastRunAt ? `上次 ${formatTime(j.lastRunAt)} · ${j.lastRunOk ? '成功' : '失败'}` : '尚未运行'}
                {j.nextRunAt ? ` · 下次 ${formatTime(j.nextRunAt)}` : ''}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// Transfers view — the same api.transfers() the desktop dock polls
// every 5s. Active transfers first; failed ones can be retried via
// api.retryTransfer. There is no cancel endpoint (desktop has none
// either), so no cancel button is offered.
function TransfersView() {
  const [transfers, setTransfers] = useState<Transfer[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const loadTransfers = useCallback(async () => {
    try {
      setTransfers(await api.transfers())
      setError(null)
    } catch (e) {
      setError(friendlyError(e))
    }
  }, [])
  useEffect(() => {
    void loadTransfers()
    const t = setInterval(() => void loadTransfers(), 5000)
    return () => clearInterval(t)
  }, [loadTransfers])
  const retry = useCallback(
    async (id: string) => {
      try {
        await api.retryTransfer(id)
        await loadTransfers()
      } catch (e) {
        setError(friendlyError(e))
      }
    },
    [loadTransfers],
  )
  const active = (t: Transfer) => t.status === 'running' || t.status === 'queued' || t.status === 'paused'
  const shown = transfers ? [...transfers].sort((a, b) => Number(active(b)) - Number(active(a))) : null
  return (
    <div>
      <div className="mhome-view-header">
        <h2 className="mhome-view-title">传输</h2>
        <button type="button" className="mhome-view-refresh" onClick={() => void loadTransfers()}>
          刷新
        </button>
      </div>
      {error ? (
        <div className="mhome-load-error" role="alert">
          <p>{error}</p>
          <button type="button" className="mhome-btn" onClick={() => void loadTransfers()}>
            重试
          </button>
        </div>
      ) : shown === null ? (
        <div className="mhome-rows" aria-busy="true">
          <div className="mhome-row-card mhome-skeleton" style={{ minHeight: 96 }} />
        </div>
      ) : shown.length === 0 ? (
        <div className="mhome-empty">
          <h2 className="mhome-empty-title">没有传输任务</h2>
          <p className="mhome-empty-text">上传、下载或复制文件后，这里会显示进度。</p>
        </div>
      ) : (
        <div className="mhome-rows" role="list" aria-label="传输任务">
          {shown.map((t) => {
            const pct = t.size > 0 ? Math.max(0, Math.min(100, Math.round((t.done / t.size) * 100))) : 0
            const failed = t.status === 'failed' || t.status === 'cancelled' || t.status === 'interrupted'
            return (
              <div className="mhome-row-card" role="listitem" key={t.id}>
                <div className="mhome-row-title">
                  {t.kind === 'upload' ? '上传' : t.kind === 'download' ? '下载' : '复制'} · {basename(t.dstRef || t.srcRef)}
                </div>
                <div className="mhome-row-sub">
                  {transferStatusLabel(t.status)}
                  {t.status === 'running' && t.speedBps > 0 ? ` · ${formatSpeed(t.speedBps)}` : ''}
                  {` · ${formatSize(t.done)} / ${formatSize(t.size)}（${pct}%）`}
                </div>
                <div className="mhome-progress" aria-label={`进度 ${pct}%`}>
                  <div style={{ width: `${pct}%` }} />
                </div>
                {t.error ? <div className="mhome-row-sub mhome-error-text">{t.error}</div> : null}
                {failed ? (
                  <div className="mhome-row-actions">
                    <button type="button" className="mhome-mini-btn" onClick={() => void retry(t.id)}>
                      重试
                    </button>
                  </div>
                ) : null}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

// Settings view — the same getSettings/saveSettings the desktop
// Settings page uses, but only the phone-sensible subset: theme,
// language, preview cache (size + limit + clear-on-exit + clear
// now). Download dir / proxy / sync engine tuning stay on desktop;
// the note below says so honestly.
function SettingsView() {
  const [settings, setSettings] = useState<Settings | null>(null)
  const [cache, setCache] = useState<PreviewCacheInfo | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const loadAll = useCallback(async () => {
    try {
      const [s, c] = await Promise.all([api.getSettings(), api.previewCacheInfo()])
      setSettings(s)
      setCache(c)
      setError(null)
    } catch (e) {
      setError(friendlyError(e))
    }
  }, [])
  useEffect(() => {
    void loadAll()
  }, [loadAll])
  const update = useCallback(
    async (patch: Partial<Settings>) => {
      if (!settings) return
      const prev = settings
      const next = { ...settings, ...patch }
      setSettings(next)
      try {
        await api.saveSettings(next)
        setSaveError(null)
      } catch (e) {
        setSettings(prev)
        setSaveError(friendlyError(e))
      }
    },
    [settings],
  )
  const clearCache = useCallback(async () => {
    try {
      await api.clearPreviewCache()
      // Clear returns only what was freed; re-read the info for the
      // current size/count/limit.
      setCache(await api.previewCacheInfo())
      setSaveError(null)
    } catch (e) {
      setSaveError(friendlyError(e))
    }
  }, [])
  const limits = [512, 1024, 2048, 5120]
  const limitLabel = (mb: number) => (mb >= 1024 ? `${mb / 1024} GB` : `${mb} MB`)
  return (
    <div>
      <div className="mhome-view-header">
        <h2 className="mhome-view-title">设置</h2>
      </div>
      {error ? (
        <div className="mhome-load-error" role="alert">
          <p>{error}</p>
          <button type="button" className="mhome-btn" onClick={() => void loadAll()}>
            重试
          </button>
        </div>
      ) : settings === null ? (
        <div className="mhome-rows" aria-busy="true">
          <div className="mhome-row-card mhome-skeleton" style={{ minHeight: 120 }} />
        </div>
      ) : (
        <div className="mhome-rows">
          <div className="mhome-row-card">
            <div className="mhome-row-title">外观</div>
            <div className="mhome-seg" role="group" aria-label="外观">
              <button
                type="button"
                className={settings.theme === 'light' ? 'mhome-seg-btn mhome-seg-active' : 'mhome-seg-btn'}
                aria-pressed={settings.theme === 'light'}
                onClick={() => void update({ theme: 'light' })}
              >
                浅色
              </button>
              <button
                type="button"
                className={settings.theme === 'dark' ? 'mhome-seg-btn mhome-seg-active' : 'mhome-seg-btn'}
                aria-pressed={settings.theme === 'dark'}
                onClick={() => void update({ theme: 'dark' })}
              >
                深色
              </button>
            </div>
          </div>
          <div className="mhome-row-card">
            <div className="mhome-row-title">语言</div>
            <div className="mhome-seg" role="group" aria-label="语言">
              <button
                type="button"
                className={settings.language === 'zh' ? 'mhome-seg-btn mhome-seg-active' : 'mhome-seg-btn'}
                aria-pressed={settings.language === 'zh'}
                onClick={() => void update({ language: 'zh' })}
              >
                中文
              </button>
              <button
                type="button"
                className={settings.language === 'en' ? 'mhome-seg-btn mhome-seg-active' : 'mhome-seg-btn'}
                aria-pressed={settings.language === 'en'}
                onClick={() => void update({ language: 'en' })}
              >
                English
              </button>
            </div>
          </div>
          <div className="mhome-row-card">
            <div className="mhome-row-title">缓存</div>
            <div className="mhome-row-sub">
              {cache
                ? `预览缓存：${formatSize(cache.sizeBytes)}（${cache.fileCount} 个文件），上限 ${limitLabel(cache.limitMB)}。`
                : '正在读取缓存占用…'}
            </div>
            <div className="mhome-row-sub">用系统应用打开文件时下载下来的副本，云端有原件，清理不会丢东西。</div>
            <div className="mhome-seg" role="group" aria-label="缓存上限">
              {limits.map((mb) => (
                <button
                  key={mb}
                  type="button"
                  className={settings.previewCacheLimitMB === mb ? 'mhome-seg-btn mhome-seg-active' : 'mhome-seg-btn'}
                  aria-pressed={settings.previewCacheLimitMB === mb}
                  onClick={() => void update({ previewCacheLimitMB: mb })}
                >
                  {limitLabel(mb)}
                </button>
              ))}
            </div>
            <div className="mhome-switch-row">
              <span>退出时清理缓存</span>
              <button
                type="button"
                className="mhome-switch"
                role="switch"
                aria-checked={settings.previewCacheClearOnExit}
                aria-label="退出时清理缓存"
                onClick={() => void update({ previewCacheClearOnExit: !settings.previewCacheClearOnExit })}
              >
                <span className="mhome-switch-knob" />
              </button>
            </div>
            <div className="mhome-row-actions">
              <button type="button" className="mhome-mini-btn" onClick={() => void clearCache()}>
                清理缓存
              </button>
            </div>
            {saveError ? <div className="mhome-row-sub mhome-error-text">{saveError}</div> : null}
          </div>
          <p className="mhome-row-sub" style={{ textAlign: 'center' }}>
            下载目录、代理、同步并发等更多设置请在桌面端调整。
          </p>
        </div>
      )}
    </div>
  )
}

type View = 'home' | 'sync' | 'transfers' | 'settings'
type Pill = 'all' | 'cloud' | 'local'
type Sheet = { kind: 'card'; mount: Mount } | { kind: 'grid' } | null

const NAV_ITEMS: { id: View; label: string; Icon: ComponentType }[] = [
  { id: 'home', label: '网盘', Icon: HomeIcon },
  { id: 'sync', label: '同步', Icon: SyncIcon },
  { id: 'transfers', label: '传输', Icon: TransferIcon },
  { id: 'settings', label: '设置', Icon: SlidersIcon },
]

export default function MobileHome() {
  const [mounts, setMounts] = useState<Mount[]>([])
  const [quotas, setQuotas] = useState<Record<string, MountQuota>>({})
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [sheet, setSheet] = useState<Sheet>(null)
  const [refreshing, setRefreshing] = useState(false)
  const [view, setView] = useState<View>('home')
  const [searchOpen, setSearchOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [pill, setPill] = useState<Pill>('all')
  const [listView, setListView] = useState(false)

  const load = useCallback(async () => {
    try {
      const ms = await api.listMounts()
      setMounts(ms)
      setLoadError(null)
    } catch (e) {
      setLoadError(friendlyError(e))
      return
    }
    try {
      const qs = await api.mountQuotas()
      const next: Record<string, MountQuota> = {}
      for (const q of qs) next[q.mountId] = q
      setQuotas(next)
    } catch {
      // Quota failure never breaks the home: cards render without
      // numbers ("容量读取中…") until the next refresh.
    }
  }, [])

  useEffect(() => {
    setLoading(true)
    void load().finally(() => setLoading(false))
  }, [load])

  const refresh = useCallback(async () => {
    setRefreshing(true)
    try {
      await load()
    } finally {
      setRefreshing(false)
    }
  }, [load])

  const goView = useCallback((v: View) => {
    setSheet(null)
    setView(v)
  }, [])

  const visibleMounts = mounts.filter((m) => {
    if (pill === 'cloud' && m.provider === 'local') return false
    if (pill === 'local' && m.provider !== 'local') return false
    const q = query.trim().toLowerCase()
    if (q && !m.displayName.toLowerCase().includes(q)) return false
    return true
  })
  const filtering = pill !== 'all' || query.trim() !== ''

  return (
    <div className="mhome">
      {view === 'home' ? (
        <>
          <header className="mhome-header">
            <div className="mhome-header-icons">
              <button type="button" className="mhome-icon-btn" aria-label="更多" onClick={() => setSheet({ kind: 'grid' })}>
                <GridIcon />
              </button>
              <button
                type="button"
                className="mhome-icon-btn"
                aria-label="搜索网盘"
                aria-expanded={searchOpen}
                onClick={() => setSearchOpen((v) => !v)}
              >
                <SearchIcon />
              </button>
            </div>
            <div className="mhome-eyebrow">管理</div>
            <h1 className="mhome-title">我的网盘</h1>
            <p className="mhome-subtitle">各网盘的容量与剩余空间，一眼看清</p>
          </header>

          {loadError ? (
            <div className="mhome-load-error" role="alert">
              <p>{loadError}</p>
              <button type="button" className="mhome-btn" onClick={() => void refresh()}>
                重试
              </button>
            </div>
          ) : loading ? (
            <>
              <div className="mhome-section">
                <span className="mhome-section-label">网盘</span>
              </div>
              <div className="mhome-carousel" aria-busy="true">
                <div className="mhome-card mhome-skeleton" />
                <div className="mhome-card mhome-skeleton" />
              </div>
            </>
          ) : mounts.length === 0 ? (
            <div className="mhome-empty">
              <h2 className="mhome-empty-title">还没有网盘</h2>
              <p className="mhome-empty-text">手机端不能添加网盘，请先在桌面端添加网盘。</p>
              <button type="button" className="mhome-btn" onClick={() => void refresh()}>
                刷新
              </button>
            </div>
          ) : (
            <>
              {searchOpen && (
                <div className="mhome-search-row">
                  <SearchIcon size={18} />
                  <input
                    type="search"
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    placeholder="搜索网盘…"
                    aria-label="搜索网盘"
                  />
                  {query && (
                    <button type="button" className="mhome-search-clear" aria-label="清除搜索" onClick={() => setQuery('')}>
                      ✕
                    </button>
                  )}
                </div>
              )}
              <div className="mhome-pills" role="group" aria-label="筛选网盘">
                <button
                  type="button"
                  className={pill === 'all' ? 'mhome-pill mhome-pill-active' : 'mhome-pill'}
                  aria-pressed={pill === 'all'}
                  onClick={() => setPill('all')}
                >
                  <GridIcon size={15} />
                  全部
                </button>
                <button
                  type="button"
                  className={pill === 'cloud' ? 'mhome-pill mhome-pill-active' : 'mhome-pill'}
                  aria-pressed={pill === 'cloud'}
                  onClick={() => setPill('cloud')}
                >
                  <CloudIcon />
                  云盘
                </button>
                <button
                  type="button"
                  className={pill === 'local' ? 'mhome-pill mhome-pill-active' : 'mhome-pill'}
                  aria-pressed={pill === 'local'}
                  onClick={() => setPill('local')}
                >
                  <FolderIcon />
                  本地
                </button>
              </div>
              <div className="mhome-section">
                <span>
                  <span className="mhome-section-label">网盘</span>
                  <span className="mhome-section-count">共 {visibleMounts.length} 个</span>
                </span>
                <button type="button" className="mhome-section-btn" onClick={() => setListView((v) => !v)}>
                  {listView ? '收起' : '查看全部'}
                </button>
              </div>
              {visibleMounts.length === 0 ? (
                <p className="mhome-no-match">{filtering ? '没有匹配的网盘' : '还没有网盘'}</p>
              ) : listView ? (
                <div className="mhome-list" role="list" aria-label="网盘存储卡片">
                  {visibleMounts.map((m) => (
                    <StorageCard key={m.id} mount={m} quota={quotas[m.id]} onMenu={(mm) => setSheet({ kind: 'card', mount: mm })} />
                  ))}
                </div>
              ) : (
                <>
                  <div className="mhome-carousel" role="list" aria-label="网盘存储卡片">
                    {visibleMounts.map((m) => (
                      <StorageCard key={m.id} mount={m} quota={quotas[m.id]} onMenu={(mm) => setSheet({ kind: 'card', mount: mm })} />
                    ))}
                  </div>
                  <p className="mhome-hint">左右滑动查看全部网盘。点卡片右上角 ··· 可刷新容量。</p>
                </>
              )}
            </>
          )}
        </>
      ) : view === 'sync' ? (
        <SyncView />
      ) : view === 'transfers' ? (
        <TransfersView />
      ) : (
        <SettingsView />
      )}

      <nav className="mhome-nav" aria-label="手机导航">
        {NAV_ITEMS.map(({ id, label, Icon }) => (
          <button
            key={id}
            type="button"
            className={view === id ? 'mhome-nav-item mhome-nav-active' : 'mhome-nav-item'}
            aria-current={view === id ? 'page' : undefined}
            onClick={() => goView(id)}
          >
            <Icon />
            <span>{label}</span>
          </button>
        ))}
      </nav>

      {sheet && (
        <div className="mhome-sheet-backdrop" onClick={() => setSheet(null)}>
          <div
            className="mhome-sheet"
            role="dialog"
            aria-label={sheet.kind === 'card' ? `${sheet.mount.displayName}的操作` : '更多操作'}
            onClick={(e) => e.stopPropagation()}
          >
            {sheet.kind === 'card' ? (
              <>
                <div className="mhome-sheet-title">{sheet.mount.displayName}</div>
                <button
                  type="button"
                  className="mhome-sheet-btn"
                  onClick={() => {
                    setSheet(null)
                    void refresh()
                  }}
                >
                  刷新容量
                </button>
              </>
            ) : (
              <>
                <div className="mhome-sheet-title">更多</div>
                <button
                  type="button"
                  className="mhome-sheet-btn"
                  onClick={() => {
                    setSheet(null)
                    void refresh()
                  }}
                >
                  刷新全部容量
                </button>
                <button
                  type="button"
                  className="mhome-sheet-btn"
                  onClick={() => {
                    setSheet(null)
                    setListView((v) => !v)
                  }}
                >
                  {listView ? '收起' : '查看全部'}
                </button>
              </>
            )}
            <button type="button" className="mhome-sheet-btn mhome-sheet-cancel" onClick={() => setSheet(null)}>
              取消
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
