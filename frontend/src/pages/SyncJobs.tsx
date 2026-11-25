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
import type { Agent, Mount, Plan, SyncJob } from '../mygo'
import { api, friendlyError, type SideSel } from '../api/client'
import { directionLabel, formatSize, formatTime, newId, runOnLabel } from '../lib/format'
import DualPane from '../components/DualPane'

const LOCAL_ROOT: SideSel = { mountId: 'local', ref: 'local:///', label: '本地 /' }

function rootSel(m: Mount): SideSel {
  return { mountId: m.id, ref: m.rootRef, label: m.displayName }
}

function parseExclude(text: string): string[] {
  return text
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l !== '')
}

function jobFromEditor(o: {
  id: string
  name: string
  src: SideSel
  dst: SideSel
  direction: SyncJob['direction']
  compare: SyncJob['compare']
  exclude: string[]
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
    schedule: '',
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
  onSaved,
  onCancel,
  onRan,
}: {
  initial: SyncJob | null
  mounts: Mount[]
  agents: Agent[]
  onSaved: (job: SyncJob) => void
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
  const [compare, setCompare] = useState<SyncJob['compare']>(initial?.compare ?? 'size+time')
  const [excludeText, setExcludeText] = useState((initial?.exclude ?? []).join('\n'))
  const [runOn, setRunOn] = useState<'local' | 'agent'>(initial?.runOn === 'agent' || initial?.runOn === 'server' ? 'agent' : 'local')
  const [agentId, setAgentId] = useState(initial?.agentId ?? '')
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
      runOn: effectiveRunOn,
      agentId: effectiveRunOn === 'agent' ? agentId : '',
      enabled: true,
      createdAt: initial?.createdAt ?? new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobId, name, src, dst, direction, compare, excludeText, effectiveRunOn, agentId])

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
        throw new Error('这个任务要在 Agent 上跑，Agent 执行在 V0.2 接入，当前只能在本机跑。先保存任务，等 Agent 执行接好后再跑。')
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
      <p className="muted" style={{ fontSize: 12, margin: '8px 0 0' }}>
        当前停留的文件夹就是同步根目录；点进子文件夹会同步那个子文件夹。
      </p>

      <div style={{ display: 'flex', gap: 24, flexWrap: 'wrap', marginTop: 16 }}>
        <div>
          <div style={{ fontSize: 13, fontWeight: 550, marginBottom: 4 }}>方向</div>
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
              <div className="hint">Agent 执行在 V0.2 接入：现在只能保存任务，还不能在 Agent 上跑。</div>
            </div>
          )}
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

      <div className="dialog-actions">
        <button className="btn" onClick={onCancel}>
          取消
        </button>
        <button className="btn" disabled={busy} onClick={() => void handleSave()}>
          保存任务
        </button>
        <button className="btn-primary" disabled={busy || planLoading} onClick={() => void handleRun()}>
          {initial ? '保存并开始同步' : '创建并开始同步'}
        </button>
      </div>
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
  const [creating, setCreating] = useState(false)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [highlightId, setHighlightId] = useState<string | null>(null)
  const [deletingId, setDeletingId] = useState<string | null>(null)
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

  const runNow = async (job: SyncJob) => {
    setError(null)
    try {
      const plan = await previewJob(job)
      if (plan.hasDeletes) {
        // Deletions need explicit confirmation in the editor.
        setExpandedId(job.id)
        setError('这个计划包含删除操作，请在下方编辑器里勾选确认后再开始同步。')
        return
      }
      if (job.runOn !== 'local') {
        setError('这个任务要在 Agent 上跑，Agent 执行在 V0.2 接入，当前只能在本机跑。')
        return
      }
      const run = await api.runSync(job, plan)
      if (!run) throw new Error('同步没有返回运行记录')
      const done = { ...job, lastRunAt: new Date().toISOString(), lastRunOk: run.status !== 'failed' }
      await api.saveSyncJob(done)
      setJobs((prev) => prev.map((j) => (j.id === job.id ? done : j)))
      onRan()
    } catch (e) {
      setError(friendlyError(e))
    }
  }

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

      {creating && (
        <SyncJobEditor
          initial={null}
          mounts={mounts}
          agents={agents}
          onSaved={afterSaved}
          onCancel={() => setCreating(false)}
          onRan={onRan}
        />
      )}

      {loading ? (
        <p className="muted">加载中…</p>
      ) : jobs.length === 0 && !creating ? (
        <div className="empty">
          <h2>还没有同步任务</h2>
          <p>点右上角“创建同步任务”，左右各选一个文件夹，就能开始同步。</p>
          <p>同步可以在本机跑，也可以指定一台服务器 Agent 跑（Agent 执行在 V0.2 接入）。</p>
        </div>
      ) : (
        jobs.map((job) => (
          <div
            key={job.id}
            ref={(el) => {
              highlightRef.current[job.id] = el
            }}
            className={highlightId === job.id ? 'job-card highlight' : 'job-card'}
          >
            <div className="job-title">{job.name}</div>
            <div className="job-meta">
              {srcLabel(job)} → {dstLabel(job)} · {directionLabel(job.direction)} · 比较：{job.compare}
              {job.exclude && job.exclude.length > 0 ? ` · 排除 ${job.exclude.length} 条规则` : ''}
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
            {expandedId === job.id && (
              <div className="accordion">
                <SyncJobEditor
                  key={job.id}
                  initial={job}
                  mounts={mounts}
                  agents={agents}
                  onSaved={afterSaved}
                  onCancel={() => setExpandedId(null)}
                  onRan={onRan}
                />
              </div>
            )}
          </div>
        ))
      )}
    </div>
  )
}
