// SPDX-License-Identifier: MIT
// PreviewPane — file preview inside the Inspector (v0.9.1, free).
// The "glance": first-screen content only, 240px tall (v0.15.0:
// 图片/视频/PDF/文本统一 240px，换个文件类型面板高度不再跳).
// fullscreen overlay (PreviewOverlay, v0.14.0) is the serious
// viewer; this pane stays small on purpose. Tier detection and
// fetching live in previewShared so glance and overlay never
// disagree about which viewer a file gets.
// Two tiers, per the 2026-11-25 decision:
//   - In-app direct preview: images, video/audio (streamed from a
//     direct URL or the loopback preview proxy — never downloaded
//     whole to disk first), PDF, plain text/code/Markdown. Rendered
//     right here in the webview.
//   - System-app open: everything else (Office, PSD, formats the
//     browser cannot render). Downloads to the preview cache first,
//     then opens with the OS default app (macOS prefers Quick
//     Look); plus a "在 Finder 中显示" entry.
// Tier is decided by extension, like the backend's previewMime
// table. A video format browsers cannot play (mkv, avi, …) is
// honestly tier 2 with the reason stated — never a broken player.
import { useEffect, useState } from 'react'
import type { Node } from '../mygo'
import { api, friendlyError } from '../api/client'
import { formatSize } from '../lib/format'
import { extOf, renderMarkdownSafe, tierOf, usePreviewSource } from './previewShared'

// revealLabel is the OS-appropriate wording for "show in the
// system file manager": the desktop runs on the user's machine,
// so the webview's user agent names the OS. Exported for the
// file context menu (FileBrowser), which offers the same entry.
export function revealLabel(): string {
  const ua = navigator.userAgent
  if (/Macintosh/.test(ua)) return '在 Finder 中显示'
  if (/Windows/.test(ua)) return '在资源管理器中显示'
  return '在文件管理器中显示'
}

export default function PreviewPane({ mountID, node, onToast }: { mountID: string; node: Node; onToast?: (message: string) => void }) {
  const tier = tierOf(node)
  const [retry, setRetry] = useState(0)
  const source = usePreviewSource(mountID, node.ref, tier, retry)
  const [mediaFailed, setMediaFailed] = useState(false)
  const [opening, setOpening] = useState(false)
  const [openedMsg, setOpenedMsg] = useState<string | null>(null)
  // System-open / reveal failures surface here (the preview
  // source itself is fine — the file just wouldn't open).
  const [openError, setOpenError] = useState<string | null>(null)
  // Markdown is rendered by marked + DOMPurify (shared with the
  // overlay) — the hand-written renderer is retired. The HTML is
  // sanitized before it touches the DOM, so a .md file from a
  // shared folder cannot inject script into the webview.
  const [mdHtml, setMdHtml] = useState<string | null>(null)
  useEffect(() => {
    let cancelled = false
    setMdHtml(null)
    if (tier !== 'markdown' || source.status !== 'ready' || source.text == null) return
    void renderMarkdownSafe(source.text).then((html) => {
      if (!cancelled) setMdHtml(html)
    })
    return () => {
      cancelled = true
    }
  }, [tier, source])
  // A new file resets the media-failure flag: a broken video must
  // not poison the next file's player.
  useEffect(() => {
    setMediaFailed(false)
    setOpenedMsg(null)
  }, [node.ref])

  const openSystem = async () => {
    setOpening(true)
    setOpenedMsg(null)
    try {
      const path = await api.openWithSystemApp(mountID, node.ref)
      setOpenedMsg(`已用系统应用打开（缓存副本：${path.split('/').pop() ?? ''}）。`)
    } catch (e) {
      setOpenError(friendlyError(e))
    } finally {
      setOpening(false)
    }
  }

  const reveal = async () => {
    setOpening(true)
    setOpenedMsg(null)
    try {
      await api.revealInFinder(mountID, node.ref)
      // v0.13.0: this confirmation is a top-right toast now, not
      // an inline message under the preview.
      onToast?.('已在文件管理器中打开所在位置。')
    } catch (e) {
      setOpenError(friendlyError(e))
    } finally {
      setOpening(false)
    }
  }

  const systemButtons = (
    <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginTop: 8 }}>
      <button className="btn btn-sm" disabled={opening} onClick={() => void openSystem()}>
        {opening ? '打开中…' : '用系统应用打开'}
      </button>
      <button className="btn btn-sm" disabled={opening} onClick={() => void reveal()}>
        {revealLabel()}
      </button>
    </div>
  )

  const url = source.status === 'ready' ? source.url ?? null : null
  const text = source.status === 'ready' ? source.text ?? null : null
  const truncated = source.status === 'ready' ? source.truncated ?? false : false

  return (
    <div style={{ marginTop: 16 }}>
      <p style={{ margin: '0 0 8px', fontSize: 13 }}>预览</p>
      {source.status === 'loading' && <p className="muted">正在加载预览…</p>}
      {source.status === 'error' && (
        <>
          <div className="error-box">{source.error}</div>
          <div style={{ display: 'flex', gap: 8, marginTop: 8 }}>
            <button className="btn btn-sm" onClick={() => setRetry((r) => r + 1)}>
              重试
            </button>
          </div>
          {systemButtons}
        </>
      )}
      {openError && <div className="error-box" style={{ marginTop: 8 }}>{openError}</div>}
      {/* 'idle' is the system / video-unplayable tier: nothing to
          fetch, only the two buttons below. The ready block's
          url/text renderers stay hidden (url/text are null). */}
      {(source.status === 'ready' || source.status === 'idle') && (
        <>
          {tier === 'image' && url && !mediaFailed && (
            <img
              src={url}
              alt={node.name}
              style={{ maxWidth: '100%', maxHeight: 240, objectFit: 'contain', display: 'block', margin: '0 auto', borderRadius: 4 }}
              onError={() => setMediaFailed(true)}
            />
          )}
          {tier === 'image' && url && mediaFailed && (
            <>
              <p className="muted">这张图片在应用内打不开（文件可能已损坏）。</p>
              {systemButtons}
            </>
          )}
          {tier === 'video' && url && !mediaFailed && (
            <video
              src={url}
              controls
              preload="metadata"
              style={{ width: '100%', maxHeight: 240, borderRadius: 4, background: '#000' }}
              onError={() => setMediaFailed(true)}
            />
          )}
          {tier === 'video' && url && mediaFailed && (
            <>
              <p className="muted">这个视频在浏览器里播不出来（可能是编码不支持）。</p>
              {systemButtons}
            </>
          )}
          {tier === 'audio' && url && !mediaFailed && (
            <audio src={url} controls preload="metadata" style={{ width: '100%' }} onError={() => setMediaFailed(true)} />
          )}
          {tier === 'audio' && url && mediaFailed && (
            <>
              <p className="muted">这个音频在浏览器里播不出来（可能是编码不支持）。</p>
              {systemButtons}
            </>
          )}
          {tier === 'pdf' && url && (
            <iframe title={node.name} src={url} style={{ width: '100%', height: 240, border: '1px solid var(--border)', borderRadius: 4 }} />
          )}
          {(tier === 'text' || tier === 'markdown') && text !== null && (
            <>
              {tier === 'markdown' ? (
                mdHtml === null ? (
                  <p className="muted">正在渲染…</p>
                ) : (
                  <div
                    className="preview-md"
                    style={{ maxHeight: 240, overflowY: 'auto', fontSize: 13, lineHeight: 1.5 }}
                    dangerouslySetInnerHTML={{ __html: mdHtml }}
                  />
                )
              ) : (
                <pre style={{ maxHeight: 240, overflow: 'auto', fontSize: 12, lineHeight: 1.5, whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: 0 }}>
                  {text}
                </pre>
              )}
              {truncated && <p className="muted">只显示了前 512 KB：文件太大，完整内容请下载后查看。</p>}
            </>
          )}
          {tier === 'video-unplayable' && (
            <>
              <p className="muted">
                浏览器打不开 .{extOf(node.name)} 这种格式（{formatSize(node.size)}），请用系统应用打开。
              </p>
              {systemButtons}
            </>
          )}
          {tier === 'system' && (
            <>
              <p className="muted">
                应用内预览不了这种格式（{formatSize(node.size)}）：先下载到本地预览缓存，再用系统默认应用打开。
              </p>
              {systemButtons}
            </>
          )}
          {(tier === 'video' || tier === 'audio') && url && !mediaFailed && (
            <p className="muted" style={{ marginTop: 8 }}>
              流式播放中：边下边播，没有先下载完整文件。
            </p>
          )}
          {openedMsg && <p className="muted">{openedMsg}</p>}
        </>
      )}
    </div>
  )
}
