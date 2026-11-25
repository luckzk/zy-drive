// AddMountDialog — the "add a drive" entry point. It does NOT fake a
// working OAuth flow: V0.1 has no Bind to register a mount with
// credentials, so the dialog states per-provider status honestly and
// stops there. Wiring the three-step OAuth wizard is the next backend
// step, not a frontend button.
import type { ProviderKind } from '../mygo'

const PROVIDERS: { kind: ProviderKind; name: string; status: string }[] = [
  { kind: 's3', name: 'S3 / R2 / 兼容 S3', status: '驱动已实现，授权流程尚未接入' },
  { kind: 'dropbox', name: 'Dropbox', status: '驱动已实现，授权流程尚未接入' },
  { kind: 'drive', name: 'Google Drive', status: '驱动已实现，授权流程尚未接入' },
  { kind: 'onedrive', name: 'OneDrive', status: '驱动在 V0.2 接入' },
  { kind: 'drime', name: 'Drime', status: '驱动在 V0.2 接入' },
]

export default function AddMountDialog({ onClose }: { onClose: () => void }) {
  return (
    <div className="dialog-backdrop" onClick={onClose}>
      <div className="dialog" onClick={(e) => e.stopPropagation()}>
        <h1>添加网盘</h1>
        <p className="muted">选一个网盘。添加网盘需要先走各家的授权（OAuth），这个流程还没接好，所以现在还加不上——不是你的操作问题。</p>
        {PROVIDERS.map((p) => (
          <div key={p.kind} className="agent-row">
            <div className="agent-main">
              <div className="agent-name">{p.name}</div>
              <div className="agent-meta">{p.status}</div>
            </div>
            <button className="btn btn-sm" disabled>
              尚未接入
            </button>
          </div>
        ))}
        <div className="notice-box">
          本地文件夹不用添加：同步任务里选源或目标时，直接选“本地文件夹”就能用。
        </div>
        <div className="dialog-actions">
          <button className="btn-primary" onClick={onClose}>
            知道了
          </button>
        </div>
      </div>
    </div>
  )
}
