// SPDX-License-Identifier: MIT
// QuotaBar — one mount's storage usage, the same rendering in the
// sidebar card, the browse header and the Inspector: never a
// different number in two places. All states are honest:
//   - quota undefined: still loading — render nothing, never a guess.
//   - quota.error: "容量暂不可用" for that mount only.
//   - quota.unlimited (S3/R2: no quota API exists): "不限量".
//   - total <= 0: the provider reports usage but no total — show
//     used only, no bar, no invented total.
//   - otherwise: "已用 X / 共 Y" + a thin bar; over 90% the fill
//     turns danger red. Local-folder mounts add "（本机磁盘）":
//     those numbers are disk space, not a cloud quota.
// compact (sidebar, ~150px text column): shorter text so the line
// never wraps mid-number — "已用 291.0 GB / 512.0 GB", "不限量"
// with the reason on the title tooltip instead of inline; the
// local-disk note gets its own line ("本机磁盘") instead of being
// squeezed onto the numbers line.
import type { MountQuota } from '../mygo'
import { formatQuotaSize } from '../lib/format'

export default function QuotaBar({ quota, compact = false }: { quota: MountQuota | undefined; compact?: boolean }) {
  if (!quota) return null
  if (quota.error) {
    // The backend maps the failure to one plain-language reason
    // (see friendlyQuotaError): sidebar keeps the short line and
    // puts the reason on the tooltip; the browse header /
    // Inspector show it inline so the user sees WHY, not just
    // that capacity is unavailable.
    return (
      <div className="quota-bar">
        <div className="quota-text" title={compact ? quota.error : undefined}>
          容量暂不可用
        </div>
        {!compact && <div className="quota-text muted">{quota.error}</div>}
      </div>
    )
  }
  if (quota.unlimited) {
    return (
      <div className="quota-text" title={compact ? 'S3 / R2 没有配额接口' : undefined}>
        {compact ? '容量：不限量' : '容量：不限量（S3 / R2 没有配额接口）'}
      </div>
    )
  }
  const usedText = `已用 ${formatQuotaSize(quota.used)}`
  const diskNote = quota.isLocalDisk ? '（本机磁盘）' : ''
  if (quota.total <= 0) {
    return (
      <div className="quota-bar">
        <div className="quota-text">
          {usedText}
          {compact ? '' : diskNote}
        </div>
        {compact && quota.isLocalDisk && <div className="quota-text">本机磁盘</div>}
      </div>
    )
  }
  const pct = Math.min(100, Math.max(0, (quota.used / quota.total) * 100))
  const hot = pct > 90
  return (
    <div className="quota-bar">
      <div className="quota-text">
        {compact ? `${usedText} / ${formatQuotaSize(quota.total)}` : `${usedText} / 共 ${formatQuotaSize(quota.total)}`}
        {compact ? '' : diskNote}
      </div>
      {compact && quota.isLocalDisk && <div className="quota-text">本机磁盘</div>}
      <div className="quota-track" role="progressbar" aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100} aria-label={`已用 ${Math.round(pct)}%`}>
        <div className={`quota-fill${hot ? ' hot' : ''}`} style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}
