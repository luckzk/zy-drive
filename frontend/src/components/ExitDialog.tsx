// SPDX-License-Identifier: MIT
// ExitDialog — 退出时有未上传文件（dirty 缓存）的确认框
// (v0.19.0, 用户 2026-11-27 决定)。
//
// 触发：App 订阅 mygo 事件 "zydrive:exit-request"（Go 侧用
// mygo.NewEvent 声明、从 OnBeforeQuit 里 PreventDefault 后发出；
// 另有同名 DOM CustomEvent 与 window.__zydriveRequestExit 可
// 供 Go 侧 Eval 触发）。对话框本身不决定退出：用户的选择经
// App 回传给 Go（__zydriveExitDecision(quit) 回调或
// "zydrive:exit-decision" DOM 事件），由 Go 侧再调 App.Quit()。
// 选“直接退出”时前端不自己退出——前端没有退出窗口的 Bind。
//
// 选择：
//   - “不退出，继续等待”：调 FlushMountDirty（后端 30 秒限时
//     刷盘），刷完如实报已传 / 还剩；还剩 >0 可再等一次；剩 0
//     则按钮变“留在应用里”。
//   - “直接退出，下次续传”：未上传的留在缓存里，下次打开续传。
// 文案写清未上传文件数与字节数；正打开的文件数一并提一句。
import type { ExitState, FlushResult } from '../mygo'
import { formatSize } from '../lib/format'
import { useClosing } from './motion'

export default function ExitDialog({
  state,
  flushing,
  flush,
  flushError,
  onWait,
  onQuitNow,
  onStay,
}: {
  state: ExitState
  flushing: boolean
  flush: FlushResult | null
  flushError: string | null
  onWait: () => void
  onQuitNow: () => void
  // 背景点掉 / 关掉 = 留在应用里，不退出。
  onStay: () => void
}) {
  const [closing, requestClose] = useClosing(onStay)
  const allFlushed = flush !== null && flush.remaining === 0
  return (
    <div className={`dialog-backdrop${closing ? ' closing' : ''}`} onClick={requestClose}>
      <div className={`dialog${closing ? ' closing' : ''}`} onClick={(e) => e.stopPropagation()}>
        <h1>退出 ZyDrive？</h1>
        <p style={{ margin: '0 0 8px' }}>
          还有 {state.dirtyCount} 个文件没上传（{formatSize(state.dirtyBytes)}）。
          {state.openFiles > 0 && `另有 ${state.openFiles} 个文件正打开着。`}
        </p>
        <p className="muted" style={{ margin: '0 0 12px' }}>
          直接退出的话，没传完的文件会留在缓存里，下次打开再续传，不会丢。等一会儿可以先把它们传上去。
        </p>
        {flush && (
          <div className="notice-box" style={{ marginBottom: 12 }}>
            {allFlushed
              ? `都传上去了（这次传了 ${flush.flushed} 个），现在退出不会丢东西。`
              : `已传上去 ${flush.flushed} 个，还剩 ${flush.remaining} 个没传上去。${flush.timedOut ? '30 秒到了，没传完的下次打开再传。' : '可以再等一次，或直接退出、下次续传。'}`}
          </div>
        )}
        {flushError && <div className="error-box">{flushError}</div>}
        <div className="dialog-actions">
          <button className="btn" disabled={flushing} onClick={onQuitNow}>
            直接退出，下次续传
          </button>
          <button className="btn-primary" disabled={flushing} onClick={onWait}>
            {flushing ? '正在上传…' : allFlushed ? '留在应用里' : '不退出，继续等待'}
          </button>
        </div>
      </div>
    </div>
  )
}
