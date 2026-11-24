// Inspector — the right-hand details panel.
// Top: generic info every cloud has (name, size, time, source).
// Below: "Advanced" collapsed by default. Professional features
// (versions, sharing, encryption, storage class, presigned URLs) are
// rendered only when the mount's Caps says the provider supports them —
// present when supported, hidden otherwise, never greyed-out and dead.
import { useState } from 'react'

type Caps = {
  supportsVersions?: boolean
  supportsShare?: boolean
  supportsEncryption?: boolean
  supportsStorageClass?: boolean
  supportsPresignedUrl?: boolean
}

const CAPS_BY_MOUNT: Record<string, Caps> = {
  'm-s3': {
    supportsVersions: true,
    supportsShare: true,
    supportsEncryption: true,
    supportsStorageClass: true,
    supportsPresignedUrl: true,
  },
  'm-dropbox': { supportsVersions: true, supportsShare: true },
  'm-drive': { supportsVersions: true, supportsShare: true },
}

export default function Inspector({ mountId }: { mountId: string }) {
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const caps = CAPS_BY_MOUNT[mountId] ?? {}

  return (
    <aside style={{ width: 280, borderLeft: '1px solid var(--border)', padding: 16, overflowY: 'auto' }}>
      <h2>详情</h2>
      <p>
        <strong>2026-11-24-行程.pdf</strong>
      </p>
      <p className="muted">大小 1.2 MB · 修改于 2026-11-24 09:41</p>
      <p className="muted">来源：{mountId}</p>

      <div style={{ marginTop: 16 }}>
        <button onClick={() => setAdvancedOpen((v) => !v)}>
          高级 {advancedOpen ? '▾' : '▸'}
        </button>
        {advancedOpen && (
          <div style={{ marginTop: 8 }}>
            {caps.supportsVersions && <p>版本：查看历史版本</p>}
            {caps.supportsShare && <p>分享：生成分享链接</p>}
            {caps.supportsEncryption && <p>加密：SSE-S3 / SSE-KMS</p>}
            {caps.supportsStorageClass && <p>存储类型：标准 / 低频访问</p>}
            {caps.supportsPresignedUrl && <p>预签名链接：生成临时下载链接</p>}
            {!caps.supportsVersions &&
              !caps.supportsShare &&
              !caps.supportsEncryption &&
              !caps.supportsStorageClass &&
              !caps.supportsPresignedUrl && <p className="muted">该网盘暂无高级项</p>}
          </div>
        )}
      </div>
    </aside>
  )
}
