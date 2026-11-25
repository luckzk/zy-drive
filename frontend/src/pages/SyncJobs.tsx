// SPDX-License-Identifier: MIT
// SyncJobs page — scheme A (Air Explorer full-screen dual-pane),
// as confirmed 2026-11-24. The page first shows the saved jobs list;
// "创建同步任务" opens the dual-pane box at the top. Clicking a
// job's "在双栏中打开" / "预览差异" expands the same dual-pane editor
// directly below that job's card, like an accordion — never at the
// top. The selected folder in each pane IS the sync root. Compare is
// recursive (backend sync.Scan) and applies the exclude rules; it
// re-runs automatically when path / direction / mode / filters
// change, without clearing. Direction and compare mode never mutate
// each other. "创建并开始同步" really saves the job, runs the plan
// and lands transfers in the dock.
//
// Jobs are persisted in SQLite via listSyncJobs/saveSyncJob. There is
// no demo data: an empty list is an honest empty state.
import { useCallback, useEffect, useRef, useState } from 'react'
import type { Agent, Mount, Plan, RunResponse, SyncJob } from '../mygo'
import { api, friendlyError, type SideSel } from '../api/client'
import { directionLabel, formatSize, formatTime, newId, runOnLabel } from '../lib/format'
import { Accordion, Skeleton, staggerStyle } from '../components/motion'
import DualPane from '../components/DualPane'

const LOCAL_ROOT: SideSel = { mountId: 'local', ref: 'local:///', label: '本地 /' }

// The default exclude rules a NEW job starts with (v0.8.3): the
// junk every cloud folder accumulates — OS thumbnails, Office
// locks, temp files, VCS and dependency trees, logs. The user can
// edit or delete any of them; empty = sync everything. These are
// not display-only: they run in the engine's compare stage
// (internal/sync/scan.go Matches, tested in scan_test.go) —
// folder rules (.git/, node_modules/) recursively exclude the
// whole subtree. Existing jobs keep their own saved rules
// (initial wins, never overwritten by these defaults).
const DEFAULT_EXCLUDE_RULES = ['.DS_Store', 'Thumbs.db', 'desktop.ini', '._*', '*.tmp', '~$*', '.git/', 'node_modules/', '*.log']

// Plain-language note per default rule, shown under the textarea
// so the list is not just a string of symbols.
const EXCLUDE_RULE_NOTES: Record<string, string> = {
  '.DS_Store': 'macOS 文件夹配置文件（图标位置、显示方式），同步了也没用',
  'Thumbs.db': 'Windows 缩略图缓存',
  'desktop.ini': 'Windows 文件夹配置文件',
  '._*': 'macOS 在外置盘 / 网盘上留下的资源叉文件',
  '*.tmp': '临时文件',
  '~$*': 'Office 打开文档时生成的临时锁文件',
  '.git/': 'Git 仓库目录（含全部历史），整个排除',
  'node_modules/': 'Node.js 依赖目录，整个排除（可重新安装）',
  '*.log': '日志文件',
}

function rootSel(m: Mount): SideSel {
  return { mountId: m.id, ref: m.rootRef, label: m.displayName }
}

function parseExclude(text: string): string[] {
  return text
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l !== '')
}

function parseSchedule(s: string | undefined): { kind: 'manual' | 'hourly' | 'daily'; time: string } {
  if (!s) return { kind: 'manual', time: '02:00' }
  if (s === 'hourly') return { kind: 'hourly', time: '02:00' }
  const m = /^daily (\d{1,2}:\d{2})$/.exec(s)
  if (m) return { kind: 'daily', time: m[1] }
  return { kind: 'manual', time: '02:00' }
}

function buildSchedule(kind: 'manual' | 'hourly' | 'daily', time: string): string {
  if (kind === 'hourly') return 'hourly'
  if (kind === 'daily') return `daily ${time}`
  return ''
}

function scheduleLabel(s: string | undefined): string {
  const p = parseSchedule(s)
  if (p.kind === 'hourly') return '每小时'
  if (p.kind === 'daily') return `每天 ${p.time}`
  return '手动'
}

function jobFromEditor(o: {
  id: string
  name: string
  src: SideSel
  dst: SideSel
  direction: SyncJob['direction']
  compare: SyncJob['compare']
  exclude: string[]
  schedule: string
  runOn: SyncJob['runOn']
  agentId: string
  enabled: boolean
  createdAt: string
  updatedAt: string
}): SyncJob {
  return {
    id: o.id,
    name: o.name.trim() || '未命名同步任务',
    srcMount: o.src.mountId,
    srcRef: o.src.ref,
    dstMount: o.dst.mountId,
    dstRef: o.dst.ref,
    direction: o.direction,
    compare: o.compare,
    exclude: o.exclude,
    schedule: o.schedule,
    runOn: o.runOn,
    agentId: o.runOn === 'local' ? '' : o.agentId || undefined,
    enabled: o.enabled,
    lastRunAt: undefined,
    lastRunOk: false,
    createdAt: o.createdAt,
    updatedAt: o.updatedAt,
  }
}

async function previewJob(job: SyncJob): Promise<Plan> {
  await api.validateJob(job)
  const plan = await api.previewSync(job)
  if (!plan) throw new Error('未能生成同步计划')
  return plan
}

function planCounts(plan: Plan) {
  let upload = 0
  let update = 0
  let del = 0
  let conflict = 0
  for (const it of plan.items) {
    if (it.action === 'upload') upload++
    else if (it.action === 'update') update++
    else if (it.action === 'delete') del++
    else if (it.action === 'conflict') conflict++
  }
  return { upload, update, del, conflict }
}

// ---- Editor: dual-pane + options + plan preview + save/run ----
function SyncJobEditor({
  initial,
  mounts,
  agents,
  defaultCompare,
  onSaved,
  onBatchSaved,
  onCancel,
  onRan,
}: {
  initial: SyncJob | null
  mounts: Mount[]
  agents: Agent[]
  // New jobs start from the Settings → 同步默认 comparison; existing
  // jobs keep their own value (initial wins, never overwritten).
  defaultCompare: SyncJob['compare']
  onSaved: (job: SyncJob) => void
  // Batch creation (multi-select): all created jobs at once, so the
  // page can add them and close the editor in one step.
  onBatchSaved?: (jobs: SyncJob[]) => void
  onCancel: () => void
  onRan: () => void
}) {
  const [jobId] = useState(initial?.id ?? newId('job'))
  const [name, setName] = useState(initial?.name ?? '')
  const [src, setSrc] = useState<SideSel>(
    initial ? { mountId: initial.srcMount, ref: initial.srcRef, label: '' } : mounts[0] ? rootSel(mounts[0]) : LOCAL_ROOT,
  )
  const [dst, setDst] = useState<SideSel>(() => {
    if (initial) return { mountId: initial.dstMount, ref: initial.dstRef, label: '' }
    const second = mounts[1] ?? mounts[0]
    return second ? rootSel(second) : LOCAL_ROOT
  })
  const [direction, setDirection] = useState<SyncJob['direction']>(initial?.direction ?? 'copy')
  const [compare, setCompare] = useState<SyncJob['compare']>(initial?.compare ?? defaultCompare)
  const [excludeText, setExcludeText] = useState(
    initial ? (initial?.exclude ?? []).join('\n') : DEFAULT_EXCLUDE_RULES.join('\n'),
  )
  const [runOn, setRunOn] = useState<'local' | 'agent'>(initial?.runOn === 'agent' || initial?.runOn === 'server' ? 'agent' : 'local')
  const [agentId, setAgentId] = useState(initial?.agentId ?? '')
  const [scheduleKind, setScheduleKind] = useState<'manual' | 'hourly' | 'daily'>(() => parseSchedule(initial?.schedule).kind)
  const [dailyTime, setDailyTime] = useState(() => parseSchedule(initial?.schedule).time)
  const schedule = buildSchedule(scheduleKind, dailyTime)
  const [plan, setPlan] = useState<Plan | null>(null)
  const [planLoading, setPlanLoading] = useState(false)
  const [planError, setPlanError] = useState<string | null>(null)
  const [confirmDeletes, setConfirmDeletes] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [doneMsg, setDoneMsg] = useState<string | null>(null)

  const hasLocalSide = src.mountId === 'local' || dst.mountId === 'local'
  const effectiveRunOn: SyncJob['runOn'] = hasLocalSide ? 'local' : runOn === 'agent' ? 'agent' : 'local'
  const exclude = parseExclude(excludeText)

  const buildJob = useCallback((): SyncJob => {
    return jobFromEditor({
      id: jobId,
      name,
      src,
      dst,
      direction,
      compare,
      exclude,
      schedule,
      runOn: effectiveRunOn,
      agentId: effectiveRunOn === 'agent' ? agentId : '',
      enabled: true,
      createdAt: initial?.createdAt ?? new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobId, name, src, dst, direction, compare, excludeText, schedule, effectiveRunOn, agentId])

  // Auto re-compare on any input change (debounced). Never clears the
  // plan on navigation; a stale plan is replaced when the new one lands.
  useEffect(() => {
    setPlanLoading(true)
    setPlanError(null)
    const t = setTimeout(() => {
      previewJob(buildJob())
        .then((p) => {
          setPlan(p)
          setPlanLoading(false)
        })
        .catch((e) => {
          setPlan(null)
          setPlanError(friendlyError(e))
          setPlanLoading(false)
        })
    }, 600)
    return () => clearTimeout(t)
  }, [buildJob])

  const save = async (): Promise<SyncJob> => {
    const saved = await api.saveSyncJob(buildJob())
    if (!saved) throw new Error('保存任务失败')
    return saved
  }

  const handleSave = async () => {
    setBusy(true)
    setError(null)
    try {
      onSaved(await save())
    } catch (e) {
      setError(friendlyError(e))
    } finally {
      setBusy(false)
    }
  }

  const handleRun = async () => {
    setBusy(true)
    setError(null)
    setDoneMsg(null)
    try {
      if (!plan) {
        const p = await previewJob(buildJob())
        setPlan(p)
        if (!p) throw new Error('未能生成同步计划')
      }
      const currentPlan = plan ?? (await previewJob(buildJob()))
      if (currentPlan.hasDeletes && !confirmDeletes) {
        throw new Error('这个计划包含删除操作，请先勾选“确认删除”再开始同步。')
      }
      if (effectiveRunOn !== 'local') {
        // Agent job: push the latest config, then start it there.
        // The plan was previewed on the desktop (both sides are
        // readable from here); execution + run record live on the
        // Agent. A mirror plan with deletes fails on the Agent by
        // design — deletions must be confirmed and run by hand.
        const saved = await save()
        await api.triggerAgentJob(saved.id)
        onSaved(saved)
        onRan()
        setDoneMsg('已在 Agent 上开始同步。运行记录在任务卡片的“运行记录”里看；桌面关掉也不影响。')
        return
      }
      const saved = await save()
      const run = await api.runSync(saved, currentPlan)
      if (!run) throw new Error('同步没有返回运行记录')
      // Record the outcome on the saved job.
      const done = { ...saved, lastRunAt: new Date().toISOString(), lastRunOk: run.status !== 'failed' }
      await api.saveSyncJob(done)
      onSaved(done)
      onRan()
      const c = planCounts(currentPlan)
      setDoneMsg(`已开始同步：待上传 ${c.upload} 个 · 待更新 ${c.update} 个 · 待删除 ${c.del} 个。进度见右下角传输胶囊。`)
    } catch (e) {
      setError(friendlyError(e))
    } finally {
      setBusy(false)
    }
  }

  // ---- Batch creation (multi-select, v0.8.3) ----
  // Semantics, decided against the existing model rather than
  // changing it: SyncJob is single-source / single-target, so a
  // batch is one job per selected entry — never one job holding
  // many entries. Source multi-select drives the batch (each
  // selected source entry = one job, all to the same target
  // root); target multi-select adds destinations (each selected
  // target entry = one more destination); both multi-selected
  // means the cartesian product, one job per (source, target)
  // pair. Folder entries are recursive (contain all contents),
  // like the sync compare itself. Job names are the typed name
  // plus " — entry name" (and " → target name" when there are
  // several targets), so the list stays readable.
  //
  // A batch is created only — never run immediately: the preview
  // above only covers the root pair, and running an un-previewed
  // batch (mirror deletes!) blind would be unsafe. The user runs
  // each created job from the list, where its own preview and
  // delete confirmation apply.
  const srcMulti = src.multi ?? []
  const dstMulti = dst.multi ?? []
  const batchSources = srcMulti.length > 0 ? srcMulti : [{ ref: src.ref, label: src.label }]
  const batchTargets = dstMulti.length > 0 ? dstMulti : [{ ref: dst.ref, label: dst.label }]
  const batchCount = batchSources.length * batchTargets.length
  const isBatch = srcMulti.length > 0 || dstMulti.length > 0

  const handleBatchCreate = async () => {
    setBusy(true)
    setError(null)
    setDoneMsg(null)
    try {
      const base = name.trim() || '未命名同步任务'
      const created: SyncJob[] = []
      for (const s of batchSources) {
        for (const d of batchTargets) {
          const jobName =
            batchCount > 1
              ? batchTargets.length > 1
                ? `${base} — ${s.label} → ${d.label}`
                : `${base} — ${s.label}`
              : base
          const job = jobFromEditor({
            id: newId('job'),
            name: jobName,
            src: { ...src, ref: s.ref, label: s.label },
            dst: { ...dst, ref: d.ref, label: d.label },
            direction,
            compare,
            exclude,
            schedule,
            runOn: effectiveRunOn,
            agentId: effectiveRunOn === 'agent' ? agentId : '',
            enabled: true,
            createdAt: new Date().toISOString(),
            updatedAt: new Date().toISOString(),
          })
          const saved = await api.saveSyncJob(job)
          if (!saved) throw new Error('保存任务失败')
          created.push(saved)
        }
      }
      if (onBatchSaved) onBatchSaved(created)
      else for (const c of created) onSaved(c)
      setDoneMsg(
        `已创建 ${created.length} 个同步任务（每项一个任务，文件夹包含其下全部内容）。只创建、没有运行：请在列表里逐个确认差异后再开始同步。`,
      )
    } catch (e) {
      setError(friendlyError(e))
    } finally {
      setBusy(false)
    }
  }

  const counts = plan ? planCounts(plan) : null
  const deletes = plan?.items.filter((it) => it.action === 'delete') ?? []

  return (
    <div className="dual-box">
      <div className="field">
        <label>任务名称</label>
        <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="例如：照片备份到 R2" />
      </div>

      <div className="dual-panes">
        <DualPane key={`src-${initial?.id ?? 'new'}`} label="源" value={src} onChange={setSrc} mounts={mounts} />
        <DualPane key={`dst-${initial?.id ?? 'new'}`} label="目标" value={dst} onChange={setDst} mounts={mounts} />
      </div>
      {/* 选中回显：创建/保存任务用的就是这两条路径，与双栏里选
          中的完全一致。没选中的行时，默认就是当前文件夹（双栏里
          标“当前文件夹”），所以这里永远有值，不存在“以为选了其
          实没选”的空态，按钮也不需要为此禁用。多选时回显批量
          数量：每项一个任务，文件夹包含其下全部内容。 */}
      <p className="sync-sel-echo" style={{ fontSize: 13, margin: '8px 0 0', wordBreak: 'break-all' }}>
        源：{src.label || '（未选）'} → 目标：{dst.label || '（未选）'}
        {isBatch && ` · 多选批量：将创建 ${batchCount} 个任务（源 ${srcMulti.length > 0 ? `${srcMulti.length} 项` : '根目录'} × 目标 ${dstMulti.length > 0 ? `${dstMulti.length} 项` : '根目录'}）`}
      </p>
      <p className="muted" style={{ fontSize: 12, margin: '4px 0 0' }}>
        单击一行选中它作为同步根目录（文件或文件夹都可以）；双击文件夹进入，点面包屑回到当前文件夹。两边各自独立选。
        勾选复选框可多选（Shift 连选、全选本页）：每项生成一个任务，文件夹包含其下全部内容。
      </p>

      <div style={{ display: 'flex', gap: 24, flexWrap: 'wrap', marginTop: 16 }}>
        <div>
          <div style={{ fontSize: 13, fontWeight: 400, marginBottom: 4 }}>方向</div>
          {(
            [
              ['copy', '单向复制：只加不删，最安全'],
              ['mirror', '单向镜像：目标与源完全一致，多出来的会删'],
              ['bidirectional', '双向同步：两边互相补齐，冲突各留一份'],
            ] as const
          ).map(([v, label]) => (
            <label key={v} className="radio-row">
              <input type="radio" name={`dir-${jobId}`} checked={direction === v} onChange={() => setDirection(v)} />
              <span>{label}</span>
            </label>
          ))}
        </div>
        <div>
          <div className="field" style={{ marginBottom: 8 }}>
            <label>比较方式</label>
            <select className="select" value={compare} onChange={(e) => setCompare(e.target.value as SyncJob['compare'])}>
              <option value="size+time">大小 + 修改时间（默认）</option>
              <option value="size">仅大小</option>
              <option value="checksum">校验值</option>
            </select>
          </div>
          <div className="field">
            <label>执行位置</label>
            <select
              className="select"
              value={effectiveRunOn}
              disabled={hasLocalSide}
              onChange={(e) => setRunOn(e.target.value as 'local' | 'agent')}
            >
              <option value="local">本机</option>
              <option value="agent">指定 Agent</option>
            </select>
            {hasLocalSide && <div className="hint">含本地文件夹，只能在本机执行，Agent 选项已置灰。</div>}
          </div>
          {effectiveRunOn === 'agent' && (
            <div className="field">
              <label>Agent</label>
              <select className="select" value={agentId} onChange={(e) => setAgentId(e.target.value)}>
                <option value="">默认 Agent</option>
                {agents.map((a) => (
                  <option key={a.id} value={a.id} disabled={!a.online}>
                    {a.name}（{a.online ? '在线' : '离线'}）
                  </option>
                ))}
              </select>
              <div className="hint">推送只传任务配置，不传凭证——凭证只随加密配置包走（设置 → Agent → 导出配置包），Agent 上没导入过包时，跑起来会提示先导包。</div>
            </div>
          )}
          <div className="field">
            <label>定时</label>
            <select
              className="select"
              value={scheduleKind}
              onChange={(e) => setScheduleKind(e.target.value as 'manual' | 'hourly' | 'daily')}
            >
              <option value="manual">手动：只在点“立即同步”时跑</option>
              <option value="hourly">每小时跑一次</option>
              <option value="daily">每天固定时间跑</option>
            </select>
            {scheduleKind === 'daily' && (
              <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginTop: 6 }}>
                <input
                  className="input"
                  style={{ width: 96 }}
                  type="time"
                  value={dailyTime}
                  onChange={(e) => setDailyTime(e.target.value)}
                />
                <span className="hint" style={{ margin: 0 }}>
                  {effectiveRunOn === 'agent' ? 'Agent 上按这个时间跑，桌面关掉也跑。' : '本机按这个时间跑（桌面要开着）。'}
                </span>
              </div>
            )}
            {scheduleKind === 'hourly' && (
              <div className="hint">{effectiveRunOn === 'agent' ? 'Agent 上每小时跑一次，桌面关掉也跑。' : '本机每小时跑一次（桌面要开着）。'}</div>
            )}
          </div>
        </div>
        <div style={{ flex: 1, minWidth: 220 }}>
          <div className="field">
            <label>过滤规则（排除规则，一行一个，留空 = 全部同步）</label>
            <textarea
              className="textarea"
              value={excludeText}
              onChange={(e) => setExcludeText(e.target.value)}
              placeholder={'*.tmp\n.DS_Store\nnode_modules'}
            />
            <div style={{ display: 'flex', gap: 8, marginTop: 6 }}>
              <button className="btn btn-sm" onClick={() => setExcludeText(DEFAULT_EXCLUDE_RULES.join('\n'))}>
                恢复默认
              </button>
              <button className="btn btn-sm" onClick={() => setExcludeText('')}>
                清空（全部同步）
              </button>
            </div>
            {/* Plain-language notes for the default rules, so the
                field is not just a string of symbols. The list
                documents the defaults; the textarea above is what
                actually runs (edit / delete freely). */}
            <ul style={{ margin: '8px 0 0', paddingLeft: 16, fontSize: 12 }} className="muted">
              {DEFAULT_EXCLUDE_RULES.map((r) => (
                <li key={r} style={{ marginBottom: 2 }}>
                  <code>{r}</code> — {EXCLUDE_RULE_NOTES[r]}
                </li>
              ))}
            </ul>
          </div>
        </div>
      </div>

      <h3 style={{ marginTop: 8 }}>差异预览</h3>
      {planLoading && <p className="muted">正在比较…</p>}
      {planError && <div className="error-box">{planError}</div>}
      {plan && counts && (
        <>
          <div className="plan-summary">
            <span>
              待上传 <b>{counts.upload}</b>
            </span>
            <span>
              待更新 <b>{counts.update}</b>
            </span>
            <span>
              待删除 <b>{counts.del}</b>
            </span>
            <span>
              冲突 <b>{counts.conflict}</b>
            </span>
            <span className="muted">已排除 {plan.filteredOut} 个（不计入）</span>
          </div>
          {plan.items.length > 0 && (
            <div className="plan-list">
              {plan.items.slice(0, 100).map((it) => (
                <div key={`${it.action}-${it.path}`} className="plan-row">
                  <span className={it.action === 'delete' ? 'badge badge-red' : it.action === 'conflict' ? 'badge badge-amber' : 'badge badge-green'}>
                    {it.action === 'upload' ? '上传' : it.action === 'update' ? '更新' : it.action === 'delete' ? '删除' : it.action === 'conflict' ? '冲突' : '跳过'}
                  </span>
                  <span className="path">{it.path}</span>
                  <span className="muted">{formatSize(it.size)}</span>
                </div>
              ))}
              {plan.items.length > 100 && <p className="muted">…还有 {plan.items.length - 100} 项</p>}
            </div>
          )}
          {plan.items.length === 0 && <p className="muted">两边已经一致，没有要同步的内容。</p>}
          {deletes.length > 0 && (
            <label className="radio-row" style={{ marginTop: 8 }}>
              <input type="checkbox" checked={confirmDeletes} onChange={(e) => setConfirmDeletes(e.target.checked)} />
              <span>
                我确认删除上面列出的 {deletes.length} 个文件（镜像/双向同步才会删文件，复制不会）。
              </span>
            </label>
          )}
        </>
      )}

      {error && <div className="error-box">{error}</div>}
      {doneMsg && <div className="notice-box" style={{ borderColor: 'var(--brand)' }}>{doneMsg}</div>}

      {isBatch && (
        <div className="notice-box" style={{ borderColor: 'var(--brand)' }}>
          多选批量创建：源已选 {srcMulti.length} 项、目标已选 {dstMulti.length} 项，将创建 {batchCount} 个任务（每项一个任务，文件夹包含其下全部内容）。
          只创建、不运行——上面差异预览只对应根目录这一对，批量任务请在列表里逐个确认后再开始同步。
        </div>
      )}

      <div className="dialog-actions">
        <button className="btn" onClick={onCancel}>
          取消
        </button>
        {isBatch ? (
          <button className="btn-primary" disabled={busy} onClick={() => void handleBatchCreate()}>
            {busy ? '创建中…' : `创建 ${batchCount} 个任务`}
          </button>
        ) : (
          <>
            <button className="btn" disabled={busy} onClick={() => void handleSave()}>
              保存任务
            </button>
            <button className="btn-primary" disabled={busy || planLoading} onClick={() => void handleRun()}>
              {initial ? '保存并开始同步' : '创建并开始同步'}
            </button>
          </>
        )}
      </div>
    </div>
  )
}

// ---- Run history: real records, newest first. Local jobs read
// the desktop database; Agent jobs ask the Agent — its database is
// the source of truth for runs that happened while the desktop
// was off. ----
function RunHistory({ runs, loading, error }: { runs: RunResponse[]; loading: boolean; error: string | null }) {
  if (loading) return <p className="muted">正在拉运行记录…</p>
  if (error) return <div className="error-box">{error}</div>
  if (runs.length === 0) return <p className="muted">还没有运行记录。</p>
  return (
    <div className="plan-list" style={{ marginTop: 8 }}>
      {runs.map((r) => {
        const run = r.run
        const p = run.plan
        return (
          <div key={run.id} className="plan-row" style={{ display: 'block' }}>
            <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
              <span className={run.status === 'completed' ? 'badge badge-green' : run.status === 'failed' ? 'badge badge-red' : 'badge badge-amber'}>
                {run.status === 'completed' ? '完成' : run.status === 'failed' ? '失败' : '运行中'}
              </span>
              <span style={{ fontSize: 13 }}>{formatTime(run.startedAt)}</span>
              {run.endedAt && <span className="muted" style={{ fontSize: 12 }}>→ {formatTime(run.endedAt)}</span>}
              {p && (
                <span className="muted" style={{ fontSize: 12 }}>
                  上传 {p.toUpload} · 更新 {p.toUpdate} · 删除 {p.toDelete}
                  {p.conflicts ? ` · 冲突 ${p.conflicts}` : ''}
                </span>
              )}
            </div>
            {run.error && <div style={{ fontSize: 12, color: 'var(--danger, #c0392b)', marginTop: 4 }}>{run.error}</div>}
            {r.items && r.items.length > 0 && (
              <div className="muted" style={{ fontSize: 12, marginTop: 4 }}>
                {r.items.slice(0, 10).map((it) => `${it.action === 'upload' ? '上传' : it.action === 'update' ? '更新' : it.action === 'delete' ? '删除' : it.action}${it.path}`).join(' · ')}
                {r.items.length > 10 && ` · …还有 ${r.items.length - 10} 项`}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

// ---- Page ----
export default function SyncJobsPage({
  mounts,
  agents,
  onRan,
}: {
  mounts: Mount[]
  agents: Agent[]
  onRan: () => void
}) {
  const [jobs, setJobs] = useState<SyncJob[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [info, setInfo] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [highlightId, setHighlightId] = useState<string | null>(null)
  const [deletingId, setDeletingId] = useState<string | null>(null)
  // New-job compare default, from Settings → 同步默认 (falls back to
  // size+time when settings can't be read, e.g. dev shell).
  const [defaultCompare, setDefaultCompare] = useState<SyncJob['compare']>('size+time')
  const highlightRef = useRef<Record<string, HTMLDivElement | null>>({})

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setJobs(await api.listSyncJobs())
    } catch (e) {
      setError(friendlyError(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
    void api
      .getSettings()
      .then((s) => setDefaultCompare(s.syncCompare))
      .catch(() => undefined)
  }, [load])

  const afterSaved = (saved: SyncJob) => {
    setJobs((prev) => {
      const i = prev.findIndex((j) => j.id === saved.id)
      if (i >= 0) {
        const next = [...prev]
        next[i] = saved
        return next
      }
      return [...prev, saved]
    })
    setCreating(false)
    setExpandedId(null)
    setHighlightId(saved.id)
    setTimeout(() => {
      highlightRef.current[saved.id]?.scrollIntoView({ behavior: 'smooth', block: 'center' })
    }, 100)
  }

  // Batch creation (multi-select): all created jobs land at once,
  // the editor closes, the first new job is highlighted, and a
  // notice says how many were created (they are created only —
  // the user runs each from the list).
  const afterBatchSaved = (created: SyncJob[]) => {
    setJobs((prev) => [...prev, ...created])
    setCreating(false)
    setExpandedId(null)
    setInfo(`已创建 ${created.length} 个同步任务（每项一个任务，文件夹包含其下全部内容）。只创建、没有运行：请逐个点“立即同步”，差异预览在每个任务里看。`)
    if (created.length > 0) {
      setHighlightId(created[0].id)
      setTimeout(() => {
        highlightRef.current[created[0].id]?.scrollIntoView({ behavior: 'smooth', block: 'center' })
      }, 100)
    }
  }

  const runNow = async (job: SyncJob) => {
    setError(null)
    try {
      if (job.runOn !== 'local') {
        // Agent job: push latest config + start it there. The run
        // record lives on the Agent; refresh the history below.
        await api.triggerAgentJob(job.id)
        setInfo(`已在 Agent 上开始同步“${job.name}”。运行记录在下方“运行记录”里看；桌面关掉也不影响。`)
        await loadRuns(job.id)
        onRan()
        return
      }
      const plan = await previewJob(job)
      if (plan.hasDeletes) {
        // Deletions need explicit confirmation in the editor.
        setExpandedId(job.id)
        setError('这个计划包含删除操作，请在下方编辑器里勾选确认后再开始同步。')
        return
      }
      const run = await api.runSync(job, plan)
      if (!run) throw new Error('同步没有返回运行记录')
      const done = { ...job, lastRunAt: new Date().toISOString(), lastRunOk: run.status !== 'failed' }
      await api.saveSyncJob(done)
      setJobs((prev) => prev.map((j) => (j.id === job.id ? done : j)))
      await loadRuns(job.id)
      onRan()
    } catch (e) {
      setError(friendlyError(e))
    }
  }

  // Run history, per job, loaded on demand (and refreshed after a run).
  const [runsOpenId, setRunsOpenId] = useState<string | null>(null)
  const [runsByJob, setRunsByJob] = useState<Record<string, RunResponse[]>>({})
  const [runsLoadingId, setRunsLoadingId] = useState<string | null>(null)
  const [runsError, setRunsError] = useState<Record<string, string>>({})
  const loadRuns = useCallback(async (jobId: string) => {
    setRunsLoadingId(jobId)
    try {
      const runs = await api.listJobRuns(jobId)
      setRunsByJob((prev) => ({ ...prev, [jobId]: runs }))
      setRunsError((prev) => {
        const next = { ...prev }
        delete next[jobId]
        return next
      })
    } catch (e) {
      setRunsError((prev) => ({ ...prev, [jobId]: friendlyError(e) }))
    } finally {
      setRunsLoadingId(null)
    }
  }, [])

  const deleteJob = async (job: SyncJob) => {
    try {
      await api.deleteSyncJob(job.id)
      setJobs((prev) => prev.filter((j) => j.id !== job.id))
      setDeletingId(null)
      if (expandedId === job.id) setExpandedId(null)
    } catch (e) {
      setError(friendlyError(e))
    }
  }

  const srcLabel = (job: SyncJob) => {
    if (job.srcMount === 'local') return `本地 ${job.srcRef.replace(/^local:\/\//, '')}`
    return mounts.find((m) => m.id === job.srcMount)?.displayName ?? job.srcMount
  }
  const dstLabel = (job: SyncJob) => {
    if (job.dstMount === 'local') return `本地 ${job.dstRef.replace(/^local:\/\//, '')}`
    return mounts.find((m) => m.id === job.dstMount)?.displayName ?? job.dstMount
  }

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 12 }}>
        <h1 style={{ margin: 0 }}>同步任务</h1>
        <span style={{ flex: 1 }} />
        <button className="btn-primary" onClick={() => setCreating(true)} disabled={creating}>
          创建同步任务
        </button>
      </div>

      {error && <div className="error-box">{error}</div>}
      {info && (
        <div className="notice-box" style={{ borderColor: 'var(--brand)' }}>
          {info}
          <button className="btn btn-sm" style={{ marginLeft: 8 }} onClick={() => setInfo(null)}>
            知道了
          </button>
        </div>
      )}

      <Accordion open={creating}>
        <SyncJobEditor
          initial={null}
          mounts={mounts}
          agents={agents}
          defaultCompare={defaultCompare}
          onSaved={afterSaved}
          onBatchSaved={afterBatchSaved}
          onCancel={() => setCreating(false)}
          onRan={onRan}
        />
      </Accordion>

      {loading ? (
        <div style={{ display: 'grid', gap: 12 }} aria-label="正在加载同步任务">
          {[0, 1].map((i) => (
            <div key={i} className="job-card">
              <Skeleton height={16} width="40%" />
              <div style={{ marginTop: 8 }}>
                <Skeleton height={12} width="75%" />
              </div>
              <div style={{ marginTop: 12, display: 'flex', gap: 8 }}>
                <Skeleton height={28} width={96} />
                <Skeleton height={28} width={96} />
              </div>
            </div>
          ))}
        </div>
      ) : jobs.length === 0 && !creating ? (
        <div className="empty">
          <h2>还没有同步任务</h2>
          <p>点右上角“创建同步任务”，左右各选一个文件夹，就能开始同步。</p>
          <p>同步可以在本机跑，也可以指定一台服务器 Agent 跑（定时、立即执行与运行记录都在 Agent 上，桌面关掉也跑）。</p>
        </div>
      ) : (
        jobs.map((job, ji) => (
          <div
            key={job.id}
            ref={(el) => {
              highlightRef.current[job.id] = el
            }}
            className={`job-card stagger-item${highlightId === job.id ? ' highlight' : ''}`}
            style={staggerStyle(ji)}
          >
            <div className="job-title">{job.name}</div>
            <div className="job-meta">
              {srcLabel(job)} → {dstLabel(job)} · {directionLabel(job.direction)} · 比较：{job.compare}
              {job.exclude && job.exclude.length > 0 ? ` · 排除 ${job.exclude.length} 条规则` : ''}
              {' · '}定时：{scheduleLabel(job.schedule)}
              {' · '}执行：{runOnLabel(job, agents)}
              {job.lastRunAt ? ` · 上次 ${formatTime(job.lastRunAt)}${job.lastRunOk ? '成功' : '失败'}` : ' · 还没跑过'}
            </div>
            <div className="job-actions">
              <button
                className="btn btn-sm"
                onClick={() => setExpandedId(expandedId === job.id ? null : job.id)}
              >
                {expandedId === job.id ? '收起双栏' : '在双栏中打开'}
              </button>
              <button
                className="btn btn-sm"
                onClick={() => setExpandedId(job.id)}
              >
                预览差异
              </button>
              <button className="btn-primary btn-sm" onClick={() => void runNow(job)}>
                立即同步
              </button>
              <button
                className="btn btn-sm"
                onClick={() => {
                  const open = runsOpenId !== job.id
                  setRunsOpenId(open ? job.id : null)
                  if (open && !runsByJob[job.id]) void loadRuns(job.id)
                }}
              >
                {runsOpenId === job.id ? '收起运行记录' : '运行记录'}
              </button>
              {deletingId === job.id ? (
                <>
                  <button className="btn-danger btn-sm" onClick={() => void deleteJob(job)}>
                    确认删除？
                  </button>
                  <button className="btn btn-sm" onClick={() => setDeletingId(null)}>
                    取消
                  </button>
                </>
              ) : (
                <button className="btn btn-sm" onClick={() => setDeletingId(job.id)}>
                  删除
                </button>
              )}
            </div>
            <Accordion open={expandedId === job.id}>
              <div className="accordion">
                <SyncJobEditor
                  key={job.id}
                  initial={job}
                  mounts={mounts}
                  agents={agents}
                  defaultCompare={defaultCompare}
                  onSaved={afterSaved}
                  onBatchSaved={afterBatchSaved}
                  onCancel={() => setExpandedId(null)}
                  onRan={onRan}
                />
              </div>
            </Accordion>
            <Accordion open={runsOpenId === job.id}>
              <div className="accordion">
                <h3 style={{ margin: '4px 0 0' }}>运行记录</h3>
                <p className="muted" style={{ fontSize: 12, margin: '4px 0 0' }}>
                  {job.runOn === 'local'
                    ? '本机运行记录（存在桌面数据库里）。'
                    : 'Agent 上的运行记录（存在 Agent 的数据库里，桌面关掉时跑的也在里面）。'}
                </p>
                <RunHistory runs={runsByJob[job.id] ?? []} loading={runsLoadingId === job.id} error={runsError[job.id] ?? null} />
              </div>
            </Accordion>
          </div>
        ))
      )}
    </div>
  )
}
