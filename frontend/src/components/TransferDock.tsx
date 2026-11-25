// SPDX-License-Identifier: MIT
// TransferDock — the transfer queue lives in a capsule at the
// bottom-right, not a permanent bottom bar. It shows the running
// count, pops open on click, and shows an unread dot when a transfer
// finished while it was closed. Inside: two tabs, 传输队列 / 最近同步.
import { useEffect, useRef, useState } from 'react'
import type { Transfer } from '../mygo'
import { api, friendlyError } from '../api/client'
import { formatSize, formatSpeed, formatTime, transferStatusLabel } from '../lib/format'
import { staggerStyle, useClosing } from './motion'
import { IconClose, IconRefresh } from './icons'

export default function TransferDock({ transfers, onRetry }: { transfers: Transfer[]; onRetry?: () => void }) {
  const [open, setOpen] = useState(false)
  const [tab, setTab] = useState<'queue' | 'recent'>('queue')
  const [unread, setUnread] = useState(false)
  const seenDoneRef = useRef(0)
  // Panel close animates out (300ms) before unmounting.
  const [panelClosing, requestPanelClose, resetPanelClosing] = useClosing(() => setOpen(false))
  const [retryMsg, setRetryMsg] = useState<string | null>(null)
  const [retryBusy, setRetryBusy] = useState(false)

  const retryOne = async (id: string) => {
    setRetryBusy(true)
    setRetryMsg(null)
    try {
      await api.retryTransfer(id)
      setRetryMsg('已重新排队：已传完的文件不会重传，只传失败的。')
      onRetry?.()
    } catch (e) {
      setRetryMsg(friendlyError(e))
    } finally {
      setRetryBusy(false)
    }
  }

  const retryAll = async () => {
    setRetryBusy(true)
    setRetryMsg(null)
    try {
      const n = await api.retryFailedTransfers()
      setRetryMsg(n === 0 ? '没有失败的传输可重试。' : `已重新排队 ${n} 个失败的传输。`)
      onRetry?.()
    } catch (e) {
      setRetryMsg(friendlyError(e))
    } finally {
      setRetryBusy(false)
    }
  }
  const togglePanel = () => {
    if (open) requestPanelClose()
    else {
      resetPanelClosing()
      setOpen(true)
    }
  }

  const running = transfers.filter((t) => t.status === 'running' || t.status === 'queued')
  const finished = transfers.filter((t) => t.status === 'completed' || t.status === 'failed' || t.status === 'cancelled' || t.status === 'interrupted')
  const failed = finished.filter((t) => t.status === 'failed' || t.status === 'cancelled' || t.status === 'interrupted')
  const doneCount = finished.length

  useEffect(() => {
    if (doneCount > seenDoneRef.current && !open) setUnread(true)
    if (open) {
      seenDoneRef.current = doneCount
      setUnread(false)
    }
  }, [doneCount, open])

  const pct = (t: Transfer) => (t.size > 0 ? Math.min(100, Math.round((t.done / t.size) * 100)) : 0)

  return (
    <>
      {open && (
        <div className={`dock-panel${panelClosing ? ' closing' : ''}`} role="dialog" aria-label="传输队列">
          <header>
            <button className={tab === 'queue' ? 'tab active' : 'tab'} onClick={() => setTab('queue')}>
              传输队列
            </button>
            <button className={tab === 'recent' ? 'tab active' : 'tab'} onClick={() => setTab('recent')}>
              最近同步
            </button>
            <span style={{ flex: 1 }} />
            <button className="icon-btn" onClick={requestPanelClose} aria-label="收起">
              <IconClose />
            </button>
          </header>
          <div className="dock-body">
            {tab === 'queue' ? (
              running.length === 0 && transfers.length === 0 ? (
                <p className="muted">暂无任务。新建同步任务并开始后，传输会出现在这里。</p>
              ) : (
                <>
                  {running.map((t, i) => (
                    <div key={t.id} className="transfer-row stagger-item" style={staggerStyle(i)}>
                      <div style={{ display: 'flex', justifyContent: 'space-between', gap: 8 }}>
                        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                          {t.kind === 'copy' ? '复制' : t.kind === 'upload' ? '上传' : '下载'} · {t.srcRef || t.srcMount} → {t.dstRef || t.dstMount}
                        </span>
                        <span className="muted" style={{ whiteSpace: 'nowrap' }}>
                          {transferStatusLabel(t.status)} {formatSpeed(t.speedBps)}
                        </span>
                      </div>
                      <div className="progress">
                        <div style={{ width: `${pct(t)}%` }} />
                      </div>
                      <div className="muted" style={{ fontSize: 12, marginTop: 4 }}>
                        {formatSize(t.done)} / {formatSize(t.size)}（{pct(t)}%）
                      </div>
                      {t.error && <div style={{ color: 'var(--danger)', fontSize: 12 }}>{t.error}</div>}
                    </div>
                  ))}
                  {running.length === 0 && <p className="muted">没有进行中的传输。</p>}
                </>
              )
            ) : finished.length === 0 ? (
              <p className="muted">还没有完成的同步记录。</p>
            ) : (
              <>
                {failed.length > 0 && (
                  <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 8 }}>
                    <span style={{ fontSize: 13 }}>{failed.length} 个失败</span>
                    <button className="btn btn-sm" disabled={retryBusy} onClick={() => void retryAll()}>
                      {retryBusy ? '重试中…' : '重试全部失败'}
                    </button>
                  </div>
                )}
                {retryMsg && <div className="notice-box" style={{ fontSize: 13 }}>{retryMsg}</div>}
                {finished.slice(-20).reverse().map((t, i) => (
                  <div key={t.id} className="transfer-row stagger-item" style={staggerStyle(i)}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', gap: 8 }}>
                      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {t.kind === 'copy' ? '复制' : t.kind === 'upload' ? '上传' : '下载'} · {t.srcRef || t.srcMount} → {t.dstRef || t.dstMount}
                      </span>
                      <span className="muted" style={{ whiteSpace: 'nowrap' }}>
                        {transferStatusLabel(t.status)} · {formatTime(t.updatedAt)}
                      </span>
                    </div>
                    <div className="muted" style={{ fontSize: 12 }}>{formatSize(t.size)}</div>
                    {t.error && <div style={{ color: 'var(--danger)', fontSize: 12 }}>{t.error}</div>}
                    {(t.status === 'failed' || t.status === 'cancelled' || t.status === 'interrupted') && (
                      <div style={{ marginTop: 6 }}>
                        <button className="btn btn-sm" disabled={retryBusy} onClick={() => void retryOne(t.id)}>
                          重试这个
                        </button>
                      </div>
                    )}
                  </div>
                ))}
                <p className="muted" style={{ fontSize: 12, marginTop: 8 }}>
                  这里是传输队列的记录；同步任务的完整运行历史在同步任务页 → 每个任务的「运行记录」里（Agent 上跑的也在里面）。
                </p>
              </>
            )}
          </div>
        </div>
      )}
      <button className="dock-capsule" onClick={togglePanel} aria-label="传输队列">
        {unread && <span className="unread-dot" />}
        <span aria-hidden style={{ display: 'inline-flex' }}>
          <IconRefresh />
        </span>
        <span>{running.length > 0 ? `传输中 ${running.length}` : '传输'}</span>
      </button>
    </>
  )
}
