// SPDX-License-Identifier: MIT
// PreviewViewers — the per-type renderers for the fullscreen
// preview overlay (v0.14.0). The shell (PreviewOverlay) owns
// navigation, chrome and toolbar state; each viewer here only
// knows how to show one kind of file, and reports failure
// upward instead of rendering a broken player. All viewers are
// keyed by file ref by the shell, so switching files remounts
// them with fresh state — no stale zoom/rotation/page leaks
// across files.
import { useEffect, useRef, useState, type MutableRefObject } from 'react'
import type { Node } from '../mygo'
import { formatSize } from '../lib/format'
import { Skeleton } from './motion'
import { FileTypeIcon } from './icons'
import { codeLanguage, extOf, highlightCode, renderMarkdownSafe } from './previewShared'
// highlight.js dark theme: the overlay is dark, so code gets
// the dark theme. It only styles .hljs elements, which the
// Inspector glance never renders.
import 'highlight.js/styles/github-dark.css'

// ---------------------------------------------------------------- image
// Click (no drag) toggles fit-to-window ↔ actual size (100%).
// Drag pans when zoomed in; wheel zooms around the current
// scale. A thumbnail (node.thumbUrl) shows first while the full
// image loads, so large images never white-screen.
export function ImageViewer({
  url,
  thumbUrl,
  scale,
  rotate,
  onScaleChange,
  onToggleFit,
  onError,
  onFitScale,
  onMaskClick,
}: {
  url: string
  thumbUrl?: string
  scale: number | 'fit'
  rotate: number
  onScaleChange: (s: number) => void
  onToggleFit: () => void
  onError: () => void
  onFitScale?: (s: number) => void
  onMaskClick?: () => void
}) {
  const wrapRef = useRef<HTMLDivElement>(null)
  const [loaded, setLoaded] = useState(false)
  const [natural, setNatural] = useState<{ w: number; h: number } | null>(null)
  const [pan, setPan] = useState({ x: 0, y: 0 })
  const dragRef = useRef<{ x: number; y: number; moved: boolean; startedAt: number } | null>(null)
  const [dragging, setDragging] = useState(false)

  // Rotation / returning to fit discards the pan: the old offset
  // means nothing in the new orientation.
  useEffect(() => {
    setPan({ x: 0, y: 0 })
  }, [rotate, scale])

  const fitScale = () => {
    const el = wrapRef.current
    if (!el || !natural) return 1
    return Math.min(1, el.clientWidth / natural.w, el.clientHeight / natural.h)
  }

  const onWheel = (e: WheelEvent) => {
    e.preventDefault()
    const base = scale === 'fit' ? fitScale() : scale
    const next = Math.min(8, Math.max(0.1, base * (e.deltaY < 0 ? 1.25 : 0.8)))
    onScaleChange(next)
  }
  // React attaches wheel listeners passively, so preventDefault
  // (stop the page scrolling under the zoom) needs a native
  // non-passive listener.
  useEffect(() => {
    const el = wrapRef.current
    if (!el) return
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scale, natural])

  const onPointerDown = (e: React.PointerEvent) => {
    dragRef.current = { x: e.clientX, y: e.clientY, moved: false, startedAt: Date.now() }
    ;(e.target as HTMLElement).setPointerCapture?.(e.pointerId)
  }
  const onPointerMove = (e: React.PointerEvent) => {
    const d = dragRef.current
    if (!d || scale === 'fit') return
    const dx = e.clientX - d.x
    const dy = e.clientY - d.y
    if (Math.abs(dx) + Math.abs(dy) > 4) d.moved = true
    if (d.moved) {
      setDragging(true)
      setPan((p) => ({ x: p.x + dx, y: p.y + dy }))
      d.x = e.clientX
      d.y = e.clientY
    }
  }
  const onPointerUp = () => {
    const d = dragRef.current
    dragRef.current = null
    setDragging(false)
    // A press without a drag is a click: toggle fit / actual.
    if (d && !d.moved && Date.now() - d.startedAt < 500) onToggleFit()
  }

  const imgStyle: React.CSSProperties =
    scale === 'fit'
      ? { maxWidth: '100%', maxHeight: '100%', objectFit: 'contain' as const }
      : natural
        ? { width: natural.w * scale, height: natural.h * scale, maxWidth: 'none', maxHeight: 'none' }
        : { maxWidth: '100%', maxHeight: '100%', objectFit: 'contain' as const }

  // Report the fitted scale to the shell so +/− zoom from "fit"
  // anchors at the actual fitted size instead of jumping to 100%.
  // The container width changes when the info panel opens, so
  // re-report on resize too.
  useEffect(() => {
    const el = wrapRef.current
    if (!el) return
    const report = () => onFitScale?.(fitScale())
    report()
    const ro = new ResizeObserver(report)
    ro.observe(el)
    return () => ro.disconnect()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [natural])

  return (
    <div
      ref={wrapRef}
      className={`preview-image-wrap${scale === 'fit' ? '' : ' zoomed'}${dragging ? ' dragging' : ''}`}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onClick={(e) => {
        // Clicking the bare area around the image closes the
        // overlay; clicks on the image itself toggle zoom (via
        // pointerup) and must not close.
        if (e.target === e.currentTarget) onMaskClick?.()
        e.stopPropagation()
      }}
    >
      {!loaded && thumbUrl && (
        <img src={thumbUrl} alt="" aria-hidden style={{ ...imgStyle, position: 'absolute', opacity: 0.5, filter: 'blur(2px)' }} />
      )}
      {!loaded && !thumbUrl && <Skeleton width="60%" height="55%" />}
      <img
        src={url}
        alt=""
        draggable={false}
        style={{
          ...imgStyle,
          transform: `translate(${pan.x}px, ${pan.y}px) rotate(${rotate}deg)`,
          visibility: loaded ? 'visible' : 'hidden',
        }}
        onLoad={(e) => {
          const img = e.currentTarget
          setNatural({ w: img.naturalWidth || 1, h: img.naturalHeight || 1 })
          setLoaded(true)
        }}
        onError={onError}
      />
    </div>
  )
}

// ---------------------------------------------------------------- media
// Native controls give progress + volume; speed and fullscreen
// come from the shell toolbar (playbackRate + requestFullscreen
// on the element, via mediaRef). Audio gets a cover placeholder
// (large type icon + track name) above the player bar — a bare
// <audio> element is a thin strip with no context.
export function MediaViewer({
  url,
  name,
  kind,
  speed,
  mediaRef,
  onError,
  onMaskClick,
}: {
  url: string
  name: string
  kind: 'video' | 'audio'
  speed: number
  mediaRef: MutableRefObject<HTMLVideoElement | HTMLAudioElement | null>
  onError: () => void
  onMaskClick?: () => void
}) {
  useEffect(() => {
    if (mediaRef.current) mediaRef.current.playbackRate = speed
  }, [speed, mediaRef])

  // Clicks on the media element itself must not close; clicks
  // on the bare area around it do.
  const onRootClick = (e: React.MouseEvent) => {
    if (e.target === e.currentTarget) onMaskClick?.()
    e.stopPropagation()
  }

  if (kind === 'audio') {
    return (
      <div className="preview-media" onClick={onRootClick}>
        <FileTypeIcon name={name} isDir={false} size={96} />
        <p style={{ margin: 0, fontSize: 15, wordBreak: 'break-all', textAlign: 'center' }}>{name}</p>
        <audio
          ref={mediaRef as MutableRefObject<HTMLAudioElement | null>}
          src={url}
          controls
          preload="metadata"
          autoPlay
          style={{ width: 'min(480px, 90%)' }}
          onError={onError}
        />
        <p className="preview-media-note">流式播放中：边下边播，没有先下载完整文件。</p>
      </div>
    )
  }
  return (
    <div className="preview-media" onClick={onRootClick}>
      <video
        ref={mediaRef as MutableRefObject<HTMLVideoElement | null>}
        src={url}
        controls
        preload="metadata"
        autoPlay
        playsInline
        onError={onError}
      />
      <p className="preview-media-note">流式播放中：边下边播，没有先下载完整文件。</p>
    </div>
  )
}

// ---------------------------------------------------------------- PDF
// PDF.js (bundled worker, vite ?url import — works in the mygo
// webview and in the harness's static server alike). Page
// navigation / zoom / fit-width are driven by the shell toolbar
// through this imperative handle; the viewer reports the current
// page/total/scale back for the toolbar display. Load failure
// reports upward — the shell shows Download + system-open.
export interface PdfApi {
  prevPage: () => void
  nextPage: () => void
  setPage: (n: number) => void
  zoomIn: () => void
  zoomOut: () => void
  fitWidth: () => void
}

export function PdfViewer({
  url,
  apiRef,
  onState,
  onError,
  onMaskClick,
}: {
  url: string
  apiRef: MutableRefObject<PdfApi | null>
  onState: (s: { page: number; total: number; scale: number }) => void
  onError: () => void
  onMaskClick?: () => void
}) {
  const wrapRef = useRef<HTMLDivElement>(null)
  const canvasRef = useRef<HTMLCanvasElement>(null)
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const docRef = useRef<any>(null)
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [scale, setScale] = useState(1)
  const [fitWidth, setFitWidth] = useState(true)
  const [failed, setFailed] = useState(false)
  const stateRef = useRef({ page, total, scale, fitWidth })
  stateRef.current = { page, total, scale, fitWidth }

  // Load the document once per URL.
  useEffect(() => {
    let cancelled = false
    setTotal(0)
    setPage(1)
    setScale(1)
    setFitWidth(true)
    setFailed(false)
    void (async () => {
      try {
        const pdfjs = await import('pdfjs-dist')
        const worker = (await import('pdfjs-dist/build/pdf.worker.min.mjs?url')).default
        if (!pdfjs.GlobalWorkerOptions.workerSrc) pdfjs.GlobalWorkerOptions.workerSrc = worker
        const task = pdfjs.getDocument({ url })
        const doc = await task.promise
        if (cancelled) {
          void doc.destroy()
          return
        }
        docRef.current = doc
        setTotal(doc.numPages)
      } catch {
        if (!cancelled) {
          setFailed(true)
          onError()
        }
      }
    })()
    return () => {
      cancelled = true
      const doc = docRef.current
      docRef.current = null
      if (doc) void doc.destroy()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [url])

  // Render the current page whenever doc/page/scale/fit changes.
  useEffect(() => {
    const doc = docRef.current
    const canvas = canvasRef.current
    const wrap = wrapRef.current
    if (!doc || !canvas || !wrap || total === 0) return
    let cancelled = false
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    let renderTask: any = null
    void (async () => {
      try {
        const p = await doc.getPage(page)
        if (cancelled) return
        const pageWidth = p.getViewport({ scale: 1 }).width
        const effScale = stateRef.current.fitWidth
          ? Math.max(0.25, (wrap.clientWidth - 32) / pageWidth)
          : stateRef.current.scale
        const viewport = p.getViewport({ scale: effScale })
        const dpr = Math.min(window.devicePixelRatio || 1, 2)
        canvas.width = Math.floor(viewport.width * dpr)
        canvas.height = Math.floor(viewport.height * dpr)
        canvas.style.width = `${Math.floor(viewport.width)}px`
        canvas.style.height = `${Math.floor(viewport.height)}px`
        const ctx = canvas.getContext('2d')
        if (!ctx) return
        renderTask = p.render({ canvasContext: ctx, viewport: viewport.clone({ scale: effScale * dpr }) })
        await renderTask.promise
        if (!cancelled) onState({ page, total, scale: effScale })
      } catch (e) {
        // A cancelled render throws RenderingCancelledException —
        // not a real failure, ignore it.
        if (!cancelled && !(e instanceof Error && e.name === 'RenderingCancelledException')) {
          setFailed(true)
          onError()
        }
      }
    })()
    return () => {
      cancelled = true
      if (renderTask) {
        try {
          renderTask.cancel()
        } catch {
          // Already finished: nothing to cancel.
        }
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page, scale, fitWidth, total])

  // The shell toolbar drives this handle; assigned every render
  // so the closures always see the latest page/total.
  useEffect(() => {
    apiRef.current = {
      prevPage: () => setPage((p) => Math.max(1, p - 1)),
      nextPage: () => setPage((p) => Math.min(total || 1, p + 1)),
      setPage: (n) => setPage(Math.min(Math.max(1, n), total || 1)),
      zoomIn: () => {
        setFitWidth(false)
        setScale((s) => Math.min(4, s * 1.25))
      },
      zoomOut: () => {
        setFitWidth(false)
        setScale((s) => Math.max(0.25, s / 1.25))
      },
      fitWidth: () => setFitWidth(true),
    }
  })

  if (failed) return null
  // Clicks on the bare area around the canvas close the overlay;
  // clicks on the canvas itself must not.
  const onRootClick = (e: React.MouseEvent) => {
    if (e.target === e.currentTarget) onMaskClick?.()
    e.stopPropagation()
  }
  if (total === 0) {
    return (
      <div className="preview-pdf" onClick={onRootClick}>
        <Skeleton width="min(480px, 80%)" height="70%" />
      </div>
    )
  }
  return (
    <div ref={wrapRef} className="preview-pdf" onClick={onRootClick}>
      <canvas ref={canvasRef} />
    </div>
  )
}

// ---------------------------------------------------------------- text
// Code: highlight.js + a line-number gutter (own column, same
// line-height as the code, sticky-left so it survives horizontal
// scroll — splitting the highlighted HTML per line would break
// multi-line spans). Plain text: plain <pre>. Markdown: marked
// + DOMPurify, same renderer as the Inspector glance. Long files
// are truncated by the backend at 512 KiB (PreviewText) — the
// notice says so explicitly, Box-style, with a download pointer.
export function TextViewer({
  text,
  truncated,
  isMarkdown,
  ext,
  fontSize,
}: {
  text: string
  truncated: boolean
  isMarkdown: boolean
  ext: string
  fontSize: number
}) {
  const [html, setHtml] = useState<string | null>(null)
  const isCode = !isMarkdown && codeLanguage(ext) !== null
  useEffect(() => {
    let cancelled = false
    setHtml(null)
    if (isMarkdown) {
      void renderMarkdownSafe(text).then((h) => {
        if (!cancelled) setHtml(h)
      })
    } else if (isCode) {
      void highlightCode(text, ext).then((h) => {
        if (!cancelled) setHtml(h)
      })
    }
    return () => {
      cancelled = true
    }
  }, [text, isMarkdown, isCode, ext])

  const lineCount = text.split('\n').length

  return (
    <div className="preview-text" style={{ fontSize }} onClick={(e) => e.stopPropagation()}>
      {isMarkdown ? (
        html === null ? (
          <Skeleton width="70%" height={14} />
        ) : (
          <div className="preview-md" dangerouslySetInnerHTML={{ __html: html }} />
        )
      ) : isCode ? (
        html === null ? (
          <Skeleton width="70%" height={14} />
        ) : (
          <div className="preview-code">
            <div className="preview-code-gutter" aria-hidden>
              {Array.from({ length: lineCount }, (_, i) => (
                <div key={i + 1}>{i + 1}</div>
              ))}
            </div>
            <pre className="preview-code-body">
              <code className="hljs" dangerouslySetInnerHTML={{ __html: html }} />
            </pre>
          </div>
        )
      ) : (
        <pre className="preview-plain">{text}</pre>
      )}
      {truncated && <p className="preview-truncated">已显示前 512 KB，下载查看全文。</p>}
    </div>
  )
}

// ---------------------------------------------------------------- system
// Formats the app cannot render in-house: file icon + size +
// reason, then Download + system-open. Never a blank area, never
// a broken player pretending to work.
export function SystemViewer({
  node,
  reason,
  onDownload,
  onSystemOpen,
  busy,
}: {
  node: Node
  reason: string
  onDownload: () => void
  onSystemOpen: () => void
  busy: boolean
}) {
  return (
    <div className="preview-fail" onClick={(e) => e.stopPropagation()}>
      <FileTypeIcon name={node.name} isDir={false} size={64} />
      <p style={{ margin: 0, fontSize: 15, wordBreak: 'break-all' }}>{node.name}</p>
      <p style={{ margin: 0 }}>
        {formatSize(node.size)} · {reason}
      </p>
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', justifyContent: 'center' }}>
        <button className="btn-primary" disabled={busy} onClick={onDownload}>
          下载
        </button>
        <button className="btn" disabled={busy} onClick={onSystemOpen}>
          用系统应用打开
        </button>
      </div>
    </div>
  )
}
