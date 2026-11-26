// SPDX-License-Identifier: MIT
// PreviewPane — file preview inside the Inspector (v0.9.1, free).
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

type Tier = 'image' | 'video' | 'audio' | 'pdf' | 'text' | 'markdown' | 'video-unplayable' | 'system'

const IMAGE_EXT = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'ico', 'avif'])
const VIDEO_EXT = new Set(['mp4', 'webm', 'mov', 'm4v'])
const VIDEO_UNPLAYABLE_EXT = new Set(['mkv', 'avi', 'wmv', 'flv', 'rmvb', 'rm'])
const AUDIO_EXT = new Set(['mp3', 'm4a', 'wav', 'ogg', 'flac', 'aac'])
const TEXT_EXT = new Set([
  'txt', 'log', 'csv', 'json', 'xml', 'yaml', 'yml', 'ini', 'cfg', 'conf', 'toml',
  'go', 'js', 'ts', 'jsx', 'tsx', 'py', 'rb', 'rs', 'java', 'c', 'h', 'cpp', 'hpp',
  'cs', 'sh', 'bash', 'sql', 'html', 'css', 'scss',
])
const MARKDOWN_EXT = new Set(['md', 'markdown'])
const TEXT_NAMES = new Set(['dockerfile', 'makefile', 'gemfile', 'rakefile', '.gitignore', '.env'])

function extOf(name: string): string {
  const i = name.lastIndexOf('.')
  return i < 0 ? '' : name.slice(i + 1).toLowerCase()
}

function tierOf(node: Node): Tier {
  const ext = extOf(node.name)
  if (IMAGE_EXT.has(ext)) return 'image'
  if (VIDEO_EXT.has(ext)) return 'video'
  if (VIDEO_UNPLAYABLE_EXT.has(ext)) return 'video-unplayable'
  if (AUDIO_EXT.has(ext)) return 'audio'
  if (ext === 'pdf') return 'pdf'
  if (MARKDOWN_EXT.has(ext)) return 'markdown'
  if (TEXT_EXT.has(ext) || TEXT_NAMES.has(node.name.toLowerCase())) return 'text'
  return 'system'
}

// Minimal Markdown renderer (in-house, no dependency): headings,
// bold/italic/code spans, links, fenced code blocks, unordered
// lists. HTML is escaped first, so file content can never inject
// markup into the page.
function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

function renderInline(s: string): string {
  let out = escapeHtml(s)
  out = out.replace(/`([^`]+)`/g, '<code>$1</code>')
  out = out.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
  out = out.replace(/\*([^*]+)\*/g, '<em>$1</em>')
  out = out.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>')
  return out
}

function renderMarkdown(src: string): string {
  const lines = src.split('\n')
  const html: string[] = []
  let inCode = false
  let inList = false
  for (const line of lines) {
    if (line.trim().startsWith('```')) {
      if (inCode) {
        html.push('</code></pre>')
        inCode = false
      } else {
        html.push('<pre><code>')
        inCode = true
      }
      continue
    }
    if (inCode) {
      html.push(escapeHtml(line))
      continue
    }
    const heading = /^(#{1,6})\s+(.*)$/.exec(line)
    if (heading) {
      if (inList) {
        html.push('</ul>')
        inList = false
      }
      const level = heading[1].length
      html.push(`<h${level} style="margin:8px 0 4px;font-size:${20 - level}px">${renderInline(heading[2])}</h${level}>`)
      continue
    }
    if (/^\s*[-*]\s+/.test(line)) {
      if (!inList) {
        html.push('<ul style="margin:4px 0;padding-left:20px">')
        inList = true
      }
      html.push(`<li>${renderInline(line.replace(/^\s*[-*]\s+/, ''))}</li>`)
      continue
    }
    if (inList) {
      html.push('</ul>')
      inList = false
    }
    if (line.trim() === '') continue
    html.push(`<p style="margin:4px 0">${renderInline(line)}</p>`)
  }
  if (inCode) html.push('</code></pre>')
  if (inList) html.push('</ul>')
  return html.join('\n')
}

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

export default function PreviewPane({ mountID, node }: { mountID: string; node: Node }) {
  const tier = tierOf(node)
  const [url, setUrl] = useState<string | null>(null)
  const [text, setText] = useState<string | null>(null)
  const [truncated, setTruncated] = useState(false)
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading')
  const [error, setError] = useState<string | null>(null)
  const [mediaFailed, setMediaFailed] = useState(false)
  const [opening, setOpening] = useState(false)
  const [openedMsg, setOpenedMsg] = useState<string | null>(null)
  // Retry counter: the effect re-runs when it changes, so the
  // "重试" button actually re-fetches instead of just flipping
  // the status text.
  const [retry, setRetry] = useState(0)

  useEffect(() => {
    let cancelled = false
    setStatus('loading')
    setError(null)
    setUrl(null)
    setText(null)
    setTruncated(false)
    setMediaFailed(false)
    setOpenedMsg(null)
    if (tier === 'system' || tier === 'video-unplayable') {
      setStatus('ready')
      return
    }
    if (tier === 'text' || tier === 'markdown') {
      void api
        .previewText(mountID, node.ref)
        .then((r) => {
          if (cancelled) return
          setText(r.text)
          setTruncated(r.truncated)
          setStatus('ready')
        })
        .catch((e) => {
          if (cancelled) return
          setError(friendlyError(e))
          setStatus('error')
        })
      return
    }
    void api
      .previewURL(mountID, node.ref)
      .then((r) => {
        if (cancelled) return
        setUrl(r.url)
        setStatus('ready')
      })
      .catch((e) => {
        if (cancelled) return
        setError(friendlyError(e))
        setStatus('error')
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mountID, node.ref, tier, retry])

  const openSystem = async () => {
    setOpening(true)
    setOpenedMsg(null)
    try {
      const path = await api.openWithSystemApp(mountID, node.ref)
      setOpenedMsg(`已用系统应用打开（缓存副本：${path.split('/').pop() ?? ''}）。`)
    } catch (e) {
      setError(friendlyError(e))
      setStatus('error')
    } finally {
      setOpening(false)
    }
  }

  const reveal = async () => {
    setOpening(true)
    setOpenedMsg(null)
    try {
      await api.revealInFinder(mountID, node.ref)
      setOpenedMsg('已在文件管理器中打开所在位置。')
    } catch (e) {
      setError(friendlyError(e))
      setStatus('error')
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

  return (
    <div style={{ marginTop: 16 }}>
      <p style={{ margin: '0 0 8px', fontSize: 13 }}>预览</p>
      {status === 'loading' && <p className="muted">正在加载预览…</p>}
      {status === 'error' && (
        <>
          <div className="error-box">{error ?? '预览加载失败。'}</div>
          <div style={{ display: 'flex', gap: 8, marginTop: 8 }}>
            <button className="btn btn-sm" onClick={() => setRetry((r) => r + 1)}>
              重试
            </button>
          </div>
          {systemButtons}
        </>
      )}
      {status === 'ready' && (
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
            <iframe title={node.name} src={url} style={{ width: '100%', height: 320, border: '1px solid var(--border)', borderRadius: 4 }} />
          )}
          {(tier === 'text' || tier === 'markdown') && text !== null && (
            <>
              {tier === 'markdown' ? (
                <div
                  style={{ maxHeight: 320, overflowY: 'auto', fontSize: 13, lineHeight: 1.5 }}
                  dangerouslySetInnerHTML={{ __html: renderMarkdown(text) }}
                />
              ) : (
                <pre style={{ maxHeight: 320, overflow: 'auto', fontSize: 12, lineHeight: 1.5, whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: 0 }}>
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
