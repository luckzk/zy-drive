// SPDX-License-Identifier: MIT
// MobileHome — the phone home screen: storage cards, first.
//
// Design reference: Adom Shafi's "Cloud Storage App" Dribbble shot
// (full-size original at ~/workspace/browser_downloads/
// sess-1810883329/...-original-16ddaec864e444fbf478d93837e438fa.png).
// Only the card PATTERN is taken from it — pastel card, brand logo
// in a white circle, name + account line, used/total + plan line,
// thick circular progress ring with % in the centre, horizontal
// snap-scroll carousel. The drawing is ours: card backgrounds are
// each card's THEME colour at 10% alpha and rings the same theme
// colour full-strength — the theme is NOT the logo brand colour:
// the reference's Drive card is warm cream with an amber ring
// (#FFF9E9 / #F5B81C), not Drive blue (measured from the shot
// 2026-11-26; see docs/mobile-app.md §二）. Name, used numbers
// and % are 700 — a deliberate, user-approved exception to the
// standing de-bold rule, scoped to .mhome only (2026-11-26).
// Hexes are re-derived from the repo's own icons.tsx brand map —
// never the shot's exact hexes, never traced artwork.
//
// Data — no mock, ever: cards render from the SAME backend calls
// the desktop sidebar uses (api.listMounts + api.mountQuotas).
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
import { api, friendlyError } from '../api/client'
import type { Mount, MountQuota } from '../mygo'
import { BrandLogo } from '../components/icons'
import { formatSize } from '../lib/format'

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

export default function MobileHome() {
  const [mounts, setMounts] = useState<Mount[]>([])
  const [quotas, setQuotas] = useState<Record<string, MountQuota>>({})
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [sheetMount, setSheetMount] = useState<Mount | null>(null)
  const [refreshing, setRefreshing] = useState(false)

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

  return (
    <div className="mhome">
      <header className="mhome-header">
        <div className="mhome-header-top">
          <div>
            <div className="mhome-eyebrow">管理</div>
            <h1 className="mhome-title">我的网盘</h1>
            <p className="mhome-subtitle">各网盘的容量与剩余空间，一眼看清</p>
          </div>
          <button
            type="button"
            className="mhome-refresh-btn"
            aria-label="刷新容量"
            onClick={() => void refresh()}
            disabled={refreshing}
          >
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M21 12a9 9 0 1 1-2.64-6.36" />
              <path d="M21 3v6h-6" />
            </svg>
          </button>
        </div>
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
          <div className="mhome-section">
            <span className="mhome-section-label">网盘</span>
            <span className="mhome-section-count">共 {mounts.length} 个</span>
          </div>
          <div className="mhome-carousel" role="list" aria-label="网盘存储卡片">
            {mounts.map((m) => (
              <StorageCard key={m.id} mount={m} quota={quotas[m.id]} onMenu={setSheetMount} />
            ))}
          </div>
          <p className="mhome-hint">左右滑动查看全部网盘。点卡片右上角 ··· 可刷新容量。</p>
        </>
      )}

      {sheetMount && (
        <div className="mhome-sheet-backdrop" onClick={() => setSheetMount(null)}>
          <div
            className="mhome-sheet"
            role="dialog"
            aria-label={`${sheetMount.displayName}的操作`}
            onClick={(e) => e.stopPropagation()}
          >
            <div className="mhome-sheet-title">{sheetMount.displayName}</div>
            <button
              type="button"
              className="mhome-sheet-btn"
              onClick={() => {
                setSheetMount(null)
                void refresh()
              }}
            >
              刷新容量
            </button>
            <button type="button" className="mhome-sheet-btn mhome-sheet-cancel" onClick={() => setSheetMount(null)}>
              取消
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
