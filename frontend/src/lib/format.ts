// SPDX-License-Identifier: MIT
// Formatting helpers — one place, so sizes and times look the same
// on every page. No mock values live here: -1 means "unknown" (dirs).

export function formatSize(size: number): string {
  if (size < 0) return '—'
  if (size === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = size
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

export function formatSpeed(bps: number): string {
  if (bps <= 0) return ''
  return `${formatSize(bps)}/s`
}

// formatQuotaSize renders a quota number in GB/TB with exactly one
// decimal ("291.0 GB", "1.0 TB"). Smaller values fall back to
// formatSize; negative means unknown.
export function formatQuotaSize(bytes: number): string {
  if (bytes < 0) return '—'
  const GB = 1024 ** 3
  const TB = 1024 ** 4
  if (bytes >= TB) return `${(bytes / TB).toFixed(1)} TB`
  if (bytes >= GB) return `${(bytes / GB).toFixed(1)} GB`
  return formatSize(bytes)
}

export function formatTime(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const pad = (n: number) => String(n).padStart(2, '0')
  const hhmm = `${pad(d.getHours())}:${pad(d.getMinutes())}`
  // 今天 / 昨天用相对文案（Finder 习惯），更早的还是绝对日期。
  // 全站统一用这一个函数，表格、网格、详情、传输记录一起受益。
  const startOf = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  const diffDays = Math.round((startOf(new Date()) - startOf(d)) / 86400000)
  if (diffDays === 0) return `今天 ${hhmm}`
  if (diffDays === 1) return `昨天 ${hhmm}`
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${hhmm}`
}

export function formatDate(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

export function directionLabel(d: string): string {
  switch (d) {
    case 'copy':
      return '单向复制'
    case 'mirror':
      return '单向镜像'
    case 'bidirectional':
      return '双向同步'
    default:
      return d
  }
}

export function runOnLabel(job: { runOn: string; agentId?: string }, agents: { id: string; name: string }[]): string {
  if (job.runOn === 'local') return '本机'
  if (job.runOn === 'agent' && job.agentId) {
    return agents.find((a) => a.id === job.agentId)?.name ?? '指定 Agent'
  }
  return '默认 Agent'
}

export function transferStatusLabel(s: string): string {
  switch (s) {
    case 'queued':
      return '排队中'
    case 'running':
      return '进行中'
    case 'paused':
      return '已暂停'
    case 'completed':
      return '已完成'
    case 'failed':
      return '失败'
    case 'cancelled':
      return '已取消'
    case 'interrupted':
      return '已中断'
    default:
      return s
  }
}

export function mountStatusLabel(s: string): string {
  switch (s) {
    case 'ok':
      return ''
    case 'auth_expired':
      return '需要重新授权'
    case 'offline':
      return '离线'
    case 'error':
      return '出错'
    default:
      return s
  }
}

export function newId(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
}
