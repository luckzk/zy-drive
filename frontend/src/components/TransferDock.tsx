// TransferDock — the transfer queue lives in a capsule at the
// bottom-right, not a permanent bottom bar. It shows the running
// count, pops open on click, and shows an unread dot when a transfer
// finished while it was closed. Inside: two tabs, 传输队列 / 最近同步.
import { useEffect, useRef, useState } from 'react'
import type { Transfer } from '../mygo'
import { formatSize, formatSpeed, formatTime, transferStatusLabel } from '../lib/format'

export default function TransferDock({ transfers }: { transfers: Transfer[] }) {
  const [open, setOpen] = useState(false)
  const [tab, setTab] = useState<'queue' | 'recent'>('queue')
  const [unread, setUnread] = useState(false)
  const seenDoneRef = useRef(0)

  const running = transfers.filter((t) => t.status === 'running' || t.status === 'queued')
  const finished = transfers.filter((t) => t.status === 'completed' || t.status === 'failed' || t.status === 'cancelled' || t.status === 'interrupted')
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
        <div className="dock-panel" role="dialog" aria-label="传输队列">
          <header>
            <button className={tab === 'queue' ? 'tab active' : 'tab'} onClick={() => setTab('queue')}>
              传输队列
            </button>
            <button className={tab === 'recent' ? 'tab active' : 'tab'} onClick={() => setTab('recent')}>
              最近同步
            </button>
            <span style={{ flex: 1 }} />
            <button className="icon-btn" onClick={() => setOpen(false)} aria-label="收起">
              ✕
            </button>
          </header>
          <div className="dock-body">
            {tab === 'queue' ? (
              running.length === 0 && transfers.length === 0 ? (
                <p className="muted">暂无任务。新建同步任务并开始后，传输会出现在这里。</p>
              ) : (
                <>
                  {running.map((t) => (
                    <div key={t.id} className="transfer-row">
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
                {finished.slice(-20).reverse().map((t) => (
                  <div key={t.id} className="transfer-row">
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
                  </div>
                ))}
                <p className="muted" style={{ fontSize: 12, marginTop: 8 }}>
                  这里只显示传输队列里的记录；同步任务的完整运行历史（runs）尚未接入。
                </p>
              </>
            )}
          </div>
        </div>
      )}
      <button className="dock-capsule" onClick={() => setOpen((v) => !v)} aria-label="传输队列">
        {unread && <span className="unread-dot" />}
        <span aria-hidden>⇅</span>
        <span>{running.length > 0 ? `传输中 ${running.length}` : '传输'}</span>
      </button>
    </>
  )
}
