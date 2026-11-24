// SyncWizard — the sync-task wizard (Air Explorer-style dual-pane).
// 2026-11-24 decision: left file manager = source, right file manager
// = target. Picking source and target side by side is easier to
// understand than two dropdowns. Steps:
//   1. Pick source (left pane) and target (right pane)
//   2. Pick direction: one-way copy / one-way mirror / bidirectional
//   3. Plan preview (uploads / updates / deletes listed; deletes
//      require explicit confirmation) → Start
// The wizard never executes without the preview step.
import { useState } from 'react'

type Mount = { id: string; displayName: string; provider: string; status: string }

function MiniPane({ title, mounts, selected, onSelect }: {
  title: string
  mounts: Mount[]
  selected: string
  onSelect: (id: string) => void
}) {
  return (
    <div className="card" style={{ flex: 1, padding: 12 }}>
      <h2>{title}</h2>
      {mounts.map((m) => (
        <div
          key={m.id}
          onClick={() => onSelect(m.id)}
          style={{
            padding: 8,
            cursor: 'pointer',
            background: m.id === selected ? '#f0fdf4' : undefined,
            borderRadius: 8,
          }}
        >
          {m.displayName} <span className="muted">({m.provider})</span>
        </div>
      ))}
      <p className="muted">/ 照片 / 2026（示例路径，Bind 接入后为真实目录）</p>
    </div>
  )
}

export default function SyncWizard({ onClose, mounts }: { onClose: () => void; mounts: Mount[] }) {
  const [src, setSrc] = useState(mounts[0]?.id ?? '')
  const [dst, setDst] = useState(mounts[1]?.id ?? '')
  const [direction, setDirection] = useState<'copy' | 'mirror' | 'bidirectional'>('copy')
  const [step, setStep] = useState<1 | 2 | 3>(1)

  return (
    <div
      style={{
        position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.3)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
      }}
    >
      <div className="card" style={{ width: 720, maxWidth: '92vw', padding: 20, background: '#fff' }}>
        <h1 style={{ marginTop: 0 }}>新建同步任务</h1>

        {step === 1 && (
          <>
            <p className="muted">左边选源，右边选目标（双栏，和 Air Explorer 一样直观）。</p>
            <div style={{ display: 'flex', gap: 12 }}>
              <MiniPane title="源" mounts={mounts} selected={src} onSelect={setSrc} />
              <MiniPane title="目标" mounts={mounts} selected={dst} onSelect={setDst} />
            </div>
          </>
        )}

        {step === 2 && (
          <>
            <label style={{ display: 'block', marginBottom: 8 }}>
              <input type="radio" name="dir" checked={direction === 'copy'} onChange={() => setDirection('copy')} />
              单向复制：只加不删，最安全
            </label>
            <label style={{ display: 'block', marginBottom: 8 }}>
              <input type="radio" name="dir" checked={direction === 'mirror'} onChange={() => setDirection('mirror')} />
              单向镜像：目标与源完全一致，多出来的会删（需预览确认）
            </label>
            <label style={{ display: 'block', marginBottom: 8 }}>
              <input type="radio" name="dir" checked={direction === 'bidirectional'} onChange={() => setDirection('bidirectional')} />
              双向同步：两边互相补齐，冲突各留一份
            </label>
            <p className="muted">执行位置：本机 / 服务器 Agent（V0.2 联调 Agent）</p>
          </>
        )}

        {step === 3 && (
          <>
            <h2>计划预览</h2>
            <p>待上传 3 个 · 待更新 1 个 · 待删除 0 个 · 冲突 0 个</p>
            {direction !== 'copy' && (
              <p className="muted">删除清单为空。如有删除项，将在此逐条列出并需你勾选确认后才执行。</p>
            )}
            <p className="muted">（V0.1 骨架：真实计划由 internal/sync 生成，此处为占位文案）</p>
          </>
        )}

        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
          <button onClick={onClose}>取消</button>
          {step > 1 && <button onClick={() => setStep((s) => (s - 1) as 1 | 2 | 3)}>上一步</button>}
          {step < 3 && (
            <button className="btn-primary" onClick={() => setStep((s) => (s + 1) as 1 | 2 | 3)}>
              下一步
            </button>
          )}
          {step === 3 && <button className="btn-primary" onClick={onClose}>开始同步</button>}
        </div>
      </div>
    </div>
  )
}
