// SPDX-License-Identifier: MIT
// Inspector — the right-hand details panel. It only renders when the
// user selected a file or folder (on-demand); otherwise the parent
// does not mount it at all, so the table takes the full width.
// Top: generic info every cloud has. Below: "高级", collapsed by
// default. Professional items are listed only when the mount's Caps
// says the provider supports them — present when supported, hidden
// otherwise, never greyed-out and dead. The actions themselves
// (generate a share link, list versions) are not wired in V0.1, and
// the panel says so instead of showing a dead button.
//
// v0.13.0: the info is a definition list (.insp-kv in tokens.css,
// Demo v8 .kv — label left, value right, grouped sections with
// dividers), not loose stacked rows. Capacity is deliberately NOT
// shown here — it lives on the sidebar mount card.
import { useEffect, useState } from 'react'
import type { Caps, Mount, Node } from '../mygo'
import { api, friendlyError } from '../api/client'
import { formatSize, formatTime } from '../lib/format'
import { Accordion } from './motion'
import { IconChevronDown, IconClose } from './icons'
import PreviewPane from './PreviewPane'

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

export default function Inspector({ node, mount, closing, onClose, openAdvancedNonce, openVersionsNonce, onChanged, onToast }: { node: Node; mount: Mount | null; closing?: boolean; onClose: () => void; openAdvancedNonce?: number; openVersionsNonce?: number; onChanged?: () => void; onToast?: (message: string) => void }) {
  const [advancedOpen, setAdvancedOpen] = useState(false)
  // “详情”右键菜单项把 nonce +1 时展开高级页。nonce 只在
  // 点“详情”时变——点别的行、手动收起后都不会误展开。
  useEffect(() => {
    if (openAdvancedNonce) setAdvancedOpen(true)
  }, [openAdvancedNonce])
  const supported = mount ? capsList(mount.caps) : []

  // 历史版本（v0.12.0）：只在支持的网盘（Caps.supportsVersions）
  // 且选中文件时出现。版本节点把版本 id 放在 extra.versionId
  // 里，恢复时原样传回后端。恢复是两步确认：点一次“恢复”
  // 变成“确认恢复？”，再点才真的恢复——恢复会覆盖当前文件
  // （旧版本仍留在历史里，所以恢复本身也可以再恢复回去）。
  const [versions, setVersions] = useState<Node[] | null>(null)
  const [versionsLoading, setVersionsLoading] = useState(false)
  const [versionsError, setVersionsError] = useState<string | null>(null)
  const [versionsMsg, setVersionsMsg] = useState<string | null>(null)
  const [confirmRestoreId, setConfirmRestoreId] = useState<string | null>(null)
  const [restoringId, setRestoringId] = useState<string | null>(null)
  // 换一个文件就清掉上一份版本列表，绝不把 A 文件的历史
  // 当成 B 文件的。
  useEffect(() => {
    setVersions(null)
    setVersionsError(null)
    setVersionsMsg(null)
    setConfirmRestoreId(null)
  }, [node.ref, mount?.id])
  const loadVersions = async () => {
    if (!mount || node.isDir) return
    setVersionsLoading(true)
    setVersionsError(null)
    setVersionsMsg(null)
    try {
      setVersions(await api.listVersions(mount.id, node.ref))
    } catch (e) {
      setVersionsError(friendlyError(e))
    } finally {
      setVersionsLoading(false)
    }
  }
  // “历史版本…”右键菜单项把 nonce +1：展开高级页并直接拉
  // 取版本列表，不用用户再点一次“查看历史版本”。
  useEffect(() => {
    if (openVersionsNonce) {
      setAdvancedOpen(true)
      void loadVersions()
    }
    // 只响应 nonce 变化；loadVersions 读的是当前 node/mount。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [openVersionsNonce])
  const handleRestore = async (versionId: string) => {
    if (!mount || confirmRestoreId !== versionId) {
      setConfirmRestoreId(versionId)
      return
    }
    setConfirmRestoreId(null)
    setRestoringId(versionId)
    setVersionsError(null)
    try {
      await api.restoreVersion(mount.id, node.ref, versionId)
      setVersionsMsg('已恢复到该版本。')
      await loadVersions()
      onChanged?.()
    } catch (e) {
      setVersionsError(friendlyError(e))
    } finally {
      setRestoringId(null)
    }
  }

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
      <p style={{ margin: '12px 0 0', fontSize: 16, wordBreak: 'break-all' }}>{node.name}</p>

      {/* 基本信息：定义列表（标签左、值右），不是松散堆叠的行。 */}
      <div className="insp-section">
        <p className="insp-section-title">基本信息</p>
        <div className="insp-kv">
          <span className="k">类型</span>
          <span className="v">{node.isDir ? '文件夹' : node.mime ?? '文件'}</span>
        </div>
        {!node.isDir && (
          <div className="insp-kv">
            <span className="k">大小</span>
            <span className="v">{formatSize(node.size)}</span>
          </div>
        )}
        <div className="insp-kv">
          <span className="k">修改时间</span>
          <span className="v">{formatTime(node.modTime)}</span>
        </div>
        <div className="insp-kv">
          <span className="k">路径</span>
          <span className="v">{node.path}</span>
        </div>
        {mount && (
          <div className="insp-kv">
            <span className="k">来源</span>
            <span className="v">{mount.displayName}</span>
          </div>
        )}
      </div>

      {node.hash && (
        <div className="insp-section">
          <p className="insp-section-title">校验值</p>
          <div className="insp-kv">
            <span className="k">{node.hashKind ?? 'hash'}</span>
            <span className="v">{node.hash}</span>
          </div>
        </div>
      )}

      {/* File preview (v0.9.1): files only, never folders. The
          pane fetches its own URL/text for the selected file and
          re-fetches when the selection changes. */}
      {mount && !node.isDir && <PreviewPane mountID={mount.id} node={node} onToast={onToast} />}

      <div style={{ marginTop: 16 }}>
        <button className="btn btn-sm" style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }} onClick={() => setAdvancedOpen((v) => !v)}>
          高级 <IconChevronDown open={advancedOpen} />
        </button>
        <Accordion open={advancedOpen}>
          <div style={{ marginTop: 8 }}>
            {supported.length > 0 ? (
              <>
                <div className="insp-kv">
                  <span className="k">支持的能力</span>
                  <span className="v">{supported.join('、')}</span>
                </div>
                {mount && mount.caps.supportsVersions && !node.isDir ? (
                  <div style={{ marginTop: 8 }}>
                    <button className="btn btn-sm" onClick={() => void loadVersions()} disabled={versionsLoading}>
                      {versions ? '刷新历史版本' : '查看历史版本'}
                    </button>
                    {versionsLoading && <p className="muted">正在读取历史版本…</p>}
                    {versionsError && <p style={{ color: 'var(--danger)' }}>{versionsError}</p>}
                    {versionsMsg && <p className="muted">{versionsMsg}</p>}
                    {versions && versions.length === 0 && <p className="muted">暂无历史版本（S3/R2 需先开启桶的版本控制）。</p>}
                    {versions && versions.length > 0 && (
                      <ul style={{ margin: '8px 0 0', paddingLeft: 18 }}>
                        {versions.map((v) => {
                          const vid = v.extra?.versionId ?? ''
                          return (
                            <li key={vid || v.modTime} style={{ marginBottom: 6 }}>
                              {formatTime(v.modTime)} · {formatSize(v.size)}{' '}
                              <button
                                className="btn btn-sm"
                                disabled={!vid || restoringId === vid}
                                onClick={() => void handleRestore(vid)}
                              >
                                {restoringId === vid ? '恢复中…' : confirmRestoreId === vid ? '确认恢复？' : '恢复'}
                              </button>
                            </li>
                          )
                        })}
                      </ul>
                    )}
                  </div>
                ) : (
                  <p className="muted">高级操作（生成分享链接等）尚未接入，后续版本提供。</p>
                )}
              </>
            ) : (
              <p className="muted">该网盘暂无高级项。</p>
            )}
            {mount && (
              <>
                <div className="insp-kv">
                  <span className="k">删除方式</span>
                  <span className="v">{mount.caps.deleteToTrash ? '移入回收站' : '永久删除'}</span>
                </div>
                {mount.caps.maxPathLen > 0 && (
                  <div className="insp-kv">
                    <span className="k">路径最长</span>
                    <span className="v">{mount.caps.maxPathLen} 字符</span>
                  </div>
                )}
              </>
            )}
          </div>
        </Accordion>
      </div>
    </aside>
  )
}
