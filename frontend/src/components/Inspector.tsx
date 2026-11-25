// Inspector — the right-hand details panel. It only renders when the
// user selected a file or folder (on-demand); otherwise the parent
// does not mount it at all, so the table takes the full width.
// Top: generic info every cloud has. Below: "高级", collapsed by
// default. Professional items are listed only when the mount's Caps
// says the provider supports them — present when supported, hidden
// otherwise, never greyed-out and dead. The actions themselves
// (generate a share link, list versions) are not wired in V0.1, and
// the panel says so instead of showing a dead button.
import { useState } from 'react'
import type { Caps, Mount, Node } from '../mygo'
import { formatSize, formatTime } from '../lib/format'
import { Accordion } from './motion'
import { IconChevronDown, IconClose } from './icons'

function capsList(caps: Caps): string[] {
  const out: string[] = []
  if (caps.supportsVersions) out.push('历史版本')
  if (caps.supportsShare) out.push('分享链接')
  if (caps.supportsEncryption) out.push('服务端加密')
  if (caps.supportsStorageClass) out.push('存储类型')
  if (caps.supportsPresignedUrl) out.push('预签名链接')
  if (caps.supportsSearch) out.push('服务端搜索')
  if (caps.supportsDelta) out.push('增量同步')
  if (caps.supportsThumbnail) out.push('缩略图')
  if (caps.supportsResumableUpload) out.push('断点续传')
  return out
}

export default function Inspector({ node, mount, closing, onClose }: { node: Node; mount: Mount | null; closing?: boolean; onClose: () => void }) {
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const supported = mount ? capsList(mount.caps) : []

  return (
    <aside
      className={closing ? 'inspector-exit' : 'inspector-enter'}
      style={{ width: 280, flexShrink: 0, borderLeft: '1px solid var(--border)', padding: 16, overflowY: 'auto' }}
    >
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <h2 style={{ margin: 0 }}>详情</h2>
        <button className="icon-btn" onClick={onClose} aria-label="关闭详情">
          <IconClose />
        </button>
      </div>
      <p style={{ marginTop: 12 }}>
        <strong>{node.name}</strong>
      </p>
      <p className="muted">
        {node.isDir ? '文件夹' : `大小 ${formatSize(node.size)}`} · 修改于 {formatTime(node.modTime)}
      </p>
      <p className="muted" style={{ wordBreak: 'break-all' }}>
        路径：{node.path}
      </p>
      {mount && <p className="muted">来源：{mount.displayName}</p>}
      {node.hash && (
        <p className="muted">
          校验值（{node.hashKind ?? 'hash'}）：{node.hash}
        </p>
      )}
      {node.mime && <p className="muted">类型：{node.mime}</p>}

      <div style={{ marginTop: 16 }}>
        <button className="btn btn-sm" style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }} onClick={() => setAdvancedOpen((v) => !v)}>
          高级 <IconChevronDown open={advancedOpen} />
        </button>
        <Accordion open={advancedOpen}>
          <div style={{ marginTop: 8 }}>
            {supported.length > 0 ? (
              <>
                <p>该网盘支持：{supported.join('、')}</p>
                <p className="muted">高级操作（生成分享链接、查看历史版本等）尚未接入，后续版本提供。</p>
              </>
            ) : (
              <p className="muted">该网盘暂无高级项。</p>
            )}
            {mount && (
              <p className="muted">
                删除方式：{mount.caps.deleteToTrash ? '移入回收站' : '永久删除'}
                {mount.caps.maxPathLen > 0 ? ` · 路径最长 ${mount.caps.maxPathLen} 字符` : ''}
              </p>
            )}
          </div>
        </Accordion>
      </div>
    </aside>
  )
}
