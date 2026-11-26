// SPDX-License-Identifier: MIT
// PreviewOverlay — the fullscreen preview overlay (v0.14.0, P1).
//
// The third preview form (after the Inspector glance and the
// system-app tier): a dark overlay covering the whole window,
// per-type viewers, file switching, adaptive toolbar, toggleable
// info panel. Visual language follows the research report
// (§四): dark mask #111111 at 92% + white text + brand green
// #56E16C accents only; overlay fade 150ms, image zoom 200ms,
// base curve cubic-bezier(0.22,1,0.36,1), transform/opacity only.
//
// Shell owns: navigation, keyboard, focus trap, toolbar state,
// download / system-open. Viewers (PreviewViewers) own only
// rendering. No share button: the app layer has no Share wiring
// at all (provider-todo §0.2 still open) — a share button with
// nothing behind it would be fake UI. No comments, no Markup,
// no self-built Office renderer, no sync/transfer panels inside
// the overlay (the transfer capsule stays as-is).
import { useEffect, useRef, useState } from 'react'
import type { Mount, Node } from '../mygo'
import { api, friendlyError } from '../api/client'
import { Skeleton, useClosing } from './motion'
import {
  IconChevronLeft,
  IconChevronRight,
  IconCopy,
  IconDownload,
  IconExpand,
  IconInfoLine,
  IconOpenExternal,
  IconRotate,
  IconXLine,
  IconZoomIn,
  IconZoomOut,
} from './icons'
import { InspectorAdvanced, InspectorBasicInfo } from './InspectorInfo'
import { tierOf, usePreviewSource } from './previewShared'
import { ImageViewer, MediaViewer, PdfViewer, SystemViewer, TextViewer, type PdfApi } from './PreviewViewers'

// Failure panel: reason + Download + system-open (+ retry when
// the failure was a fetch, not a broken file). Never a blank area.
function PreviewFail({
  reason,
  onDownload,
  onSystemOpen,
  onRetry,
  busy,
}: {
  reason: string
  onDownload: () => void
  onSystemOpen: () => void
  onRetry?: () => void
  busy: boolean
}) {
  return (
    <div className="preview-fail" onClick={(e) => e.stopPropagation()}>
      <p style={{ margin: 0, fontSize: 15 }}>{reason}</p>
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', justifyContent: 'center' }}>
        {onRetry && (
          <button className="btn" disabled={busy} onClick={onRetry}>
            重试
          </button>
        )}
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

const SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 2]

export default function PreviewOverlay({
  mount,
  files,
  index,
  onNavigate,
  onClose,
  onToast,
}: {
  mount: Mount
  files: Node[]
  index: number
  onNavigate: (i: number) => void
  onClose: () => void
  onToast?: (message: string) => void
}) {
  const node = files[index]
  const tier = tierOf(node)
  const [retry, setRetry] = useState(0)
  const source = usePreviewSource(mount.id, node.ref, tier, retry)
  const [closing, requestClose] = useClosing(onClose, 150)
  const rootRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)

  // Per-file viewer state (reset on every navigation — zoom,
  // rotation, font size and speed never leak across files).
  const [viewerFailed, setViewerFailed] = useState(false)
  const [infoOpen, setInfoOpen] = useState(false)
  const [imgScale, setImgScale] = useState<number | 'fit'>('fit')
  const [imgRotate, setImgRotate] = useState(0)
  const [textFontSize, setTextFontSize] = useState(14)
  const [mediaSpeed, setMediaSpeed] = useState(1)
  const mediaRef = useRef<HTMLVideoElement | HTMLAudioElement | null>(null)
  // The image viewer's current fitted scale (reported by the
  // viewer as container / natural size change), used to anchor
  // +/− zoom when starting from "fit". Ref only: the shell never
  // renders from it.
  const fitScaleRef = useRef(1)
  const [pdfState, setPdfState] = useState<{ page: number; total: number; scale: number } | null>(null)
  const pdfApiRef = useRef<PdfApi | null>(null)
  const [busy, setBusy] = useState(false)
  // Bottom toolbar: visible on mouse activity, fades out after
  // 2s of stillness (report §四). The top bar stays visible.
  const [chromeHidden, setChromeHidden] = useState(false)
  const idleTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    setViewerFailed(false)
    setImgScale('fit')
    setImgRotate(0)
    setTextFontSize(14)
    setMediaSpeed(1)
    setPdfState(null)
  }, [index])

  // Focus + scroll memory: focus the overlay on open (for the
  // focus trap + keyboard), restore focus and scroll on close.
  // The overlay is fixed, so the list underneath never moves —
  // this just makes the round-trip explicit.
  useEffect(() => {
    const prevFocus = document.activeElement as HTMLElement | null
    const prevScroll = window.scrollY
    rootRef.current?.focus()
    return () => {
      window.scrollTo(0, prevScroll)
      prevFocus?.focus?.()
    }
  }, [])

  const pokeChrome = () => {
    setChromeHidden(false)
    if (idleTimer.current) clearTimeout(idleTimer.current)
    idleTimer.current = setTimeout(() => setChromeHidden(true), 2000)
  }
  useEffect(() => {
    pokeChrome()
    return () => {
      if (idleTimer.current) clearTimeout(idleTimer.current)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const hasPrev = index > 0
  const hasNext = index < files.length - 1
  const goPrev = () => {
    if (hasPrev) onNavigate(index - 1)
  }
  const goNext = () => {
    if (hasNext) onNavigate(index + 1)
  }

  const handleDownload = async () => {
    if (busy) return
    setBusy(true)
    try {
      const settings = await api.getSettings()
      const dir = settings.downloadDir?.trim()
      if (!dir) {
        onToast?.('还没有设置默认下载文件夹：先去设置 → 通用里填一个，批量下载会下到那里。')
        return
      }
      const res = await api.batchDownload(mount.id, [node.ref], dir)
      if (!res) throw new Error('批量下载没有返回结果。')
      onToast?.(`已把 ${node.name} 加入下载队列，进度见右下角传输胶囊。`)
    } catch (e) {
      onToast?.(friendlyError(e))
    } finally {
      setBusy(false)
    }
  }

  const handleSystemOpen = async () => {
    if (busy) return
    setBusy(true)
    try {
      await api.openWithSystemApp(mount.id, node.ref)
      onToast?.(`已用系统应用打开 ${node.name}。`)
    } catch (e) {
      onToast?.(friendlyError(e))
    } finally {
      setBusy(false)
    }
  }

  const handleCopyText = async () => {
    const t = source.status === 'ready' ? source.text ?? '' : ''
    try {
      await navigator.clipboard.writeText(t)
      onToast?.('已复制全文。')
    } catch {
      onToast?.('复制失败：浏览器不允许访问剪贴板。')
    }
  }

  // + / − zoom, dispatched by tier (image scale, PDF scale,
  // text font size). From fit, image zoom starts at the fitted
  // scale the viewer reported — not at 100%, which would jump.
  const zoomIn = () => {
    if (tier === 'image') setImgScale((s) => (s === 'fit' ? Math.min(8, fitScaleRef.current * 1.25) : Math.min(8, s * 1.25)))
    else if (tier === 'pdf') pdfApiRef.current?.zoomIn()
    else if (tier === 'text' || tier === 'markdown') setTextFontSize((f) => Math.min(24, f + 2))
  }
  const zoomOut = () => {
    if (tier === 'image') setImgScale((s) => (s === 'fit' ? Math.max(0.1, fitScaleRef.current / 1.25) : Math.max(0.1, s / 1.25)))
    else if (tier === 'pdf') pdfApiRef.current?.zoomOut()
    else if (tier === 'text' || tier === 'markdown') setTextFontSize((f) => Math.max(10, f - 2))
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    // Focus trap: Tab cycles inside the overlay, never out.
    if (e.key === 'Tab') {
      const root = rootRef.current
      if (!root) return
      const items = [...root.querySelectorAll<HTMLElement>('button, input, select, textarea, [tabindex]:not([tabindex="-1"])')].filter(
        (el) => !el.hasAttribute('disabled') && el.offsetParent !== null,
      )
      if (items.length === 0) return
      const first = items[0]
      const last = items[items.length - 1]
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault()
        first.focus()
      }
      return
    }
    // Typing in the PDF page input (or any input) must not
    // trigger overlay shortcuts — except Esc, which always closes.
    const target = e.target as HTMLElement | null
    const inField = !!target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT')
    if (e.key === 'Escape') {
      e.preventDefault()
      requestClose()
      return
    }
    if (inField) return
    if (e.key === 'ArrowLeft') {
      e.preventDefault()
      goPrev()
    } else if (e.key === 'ArrowRight') {
      e.preventDefault()
      goNext()
    } else if (e.key === '+' || e.key === '=') {
      e.preventDefault()
      zoomIn()
    } else if (e.key === '-' || e.key === '_') {
      e.preventDefault()
      zoomOut()
    } else if (e.key === 'i' || e.key === 'I') {
      e.preventDefault()
      setInfoOpen((v) => !v)
    } else if (e.key === 'd' || e.key === 'D') {
      e.preventDefault()
      void handleDownload()
    }
  }

  const url = source.status === 'ready' ? source.url ?? null : null
  const text = source.status === 'ready' ? source.text ?? null : null
  const truncated = source.status === 'ready' ? source.truncated ?? false : false

  const failReason =
    tier === 'video-unplayable'
      ? `浏览器打不开 .${node.name.split('.').pop() ?? ''} 这种格式，请用系统应用打开。`
      : tier === 'system'
        ? '应用内预览不了这种格式：先下载到本地预览缓存，再用系统默认应用打开。'
        : tier === 'image'
          ? '这张图片在应用内打不开（文件可能已损坏）。'
          : tier === 'video' || tier === 'audio'
            ? '这个媒体在浏览器里播不出来（可能是编码不支持）。'
            : '预览加载失败。'

  return (
    <div
      ref={rootRef}
      className={`preview-overlay${closing ? ' closing' : ''}`}
      role="dialog"
      aria-modal="true"
      aria-label={`预览 ${node.name}`}
      tabIndex={-1}
      onKeyDown={onKeyDown}
      onMouseMove={pokeChrome}
    >
      {/* Top bar: info toggle left, filename centered (ellipsis),
          download / system-open / close right. No share button —
          there is no Share wiring behind it (provider-todo §0.2). */}
      <div className="preview-topbar">
        <button
          type="button"
          className={`preview-icon-btn${infoOpen ? ' active' : ''}`}
          aria-label="文件信息"
          aria-pressed={infoOpen}
          title="文件信息 (i)"
          onClick={() => setInfoOpen((v) => !v)}
        >
          <IconInfoLine size={18} />
        </button>
        <span className="preview-title" title={node.name}>
          {node.name}
        </span>
        <button type="button" className="preview-icon-btn" aria-label="下载" title="下载 (d)" disabled={busy} onClick={() => void handleDownload()}>
          <IconDownload size={18} />
        </button>
        <button
          type="button"
          className="preview-icon-btn"
          aria-label="用系统应用打开"
          title="用系统应用打开"
          disabled={busy}
          onClick={() => void handleSystemOpen()}
        >
          <IconOpenExternal size={18} />
        </button>
        <button type="button" className="preview-icon-btn" aria-label="关闭预览" title="关闭 (Esc)" onClick={requestClose}>
          <IconXLine size={18} />
        </button>
      </div>

      <div className="preview-main">
        <div
          ref={contentRef}
          className="preview-content"
          onClick={(e) => {
            // Clicking the bare mask area (not a viewer child)
            // closes; viewers stop their own clicks from reaching
            // here, except the image/pdf/media roots, which close
            // only when the click lands on the root itself.
            if (e.target === e.currentTarget) requestClose()
          }}
        >
          {source.status === 'loading' && (
            <div className="preview-skeleton">
              <Skeleton width="72%" height={20} />
              <div style={{ marginTop: 16 }}>
                <Skeleton width="100%" height={220} />
              </div>
              <div style={{ marginTop: 12 }}>
                <Skeleton width="60%" height={14} />
              </div>
            </div>
          )}
          {source.status === 'error' && (
            <PreviewFail
              reason={source.error}
              onDownload={() => void handleDownload()}
              onSystemOpen={() => void handleSystemOpen()}
              onRetry={() => setRetry((r) => r + 1)}
              busy={busy}
            />
          )}
          {viewerFailed && source.status !== 'error' && (
            <PreviewFail
              reason={failReason}
              onDownload={() => void handleDownload()}
              onSystemOpen={() => void handleSystemOpen()}
              busy={busy}
            />
          )}
          {!viewerFailed && source.status === 'ready' && (
            <>
              {tier === 'image' && url && (
                <ImageViewer
                  key={node.ref}
                  url={url}
                  thumbUrl={node.thumbUrl}
                  scale={imgScale}
                  rotate={imgRotate}
                  onScaleChange={setImgScale}
                  onToggleFit={() => setImgScale((s) => (s === 'fit' ? 1 : 'fit'))}
                  onError={() => setViewerFailed(true)}
                  onFitScale={(s) => {
                    fitScaleRef.current = s
                  }}
                  onMaskClick={requestClose}
                />
              )}
              {(tier === 'video' || tier === 'audio') && url && (
                <MediaViewer
                  key={node.ref}
                  url={url}
                  name={node.name}
                  kind={tier}
                  speed={mediaSpeed}
                  mediaRef={mediaRef}
                  onError={() => setViewerFailed(true)}
                  onMaskClick={requestClose}
                />
              )}
              {tier === 'pdf' && url && (
                <PdfViewer
                  key={node.ref}
                  url={url}
                  apiRef={pdfApiRef}
                  onState={setPdfState}
                  onError={() => setViewerFailed(true)}
                  onMaskClick={requestClose}
                />
              )}
              {(tier === 'text' || tier === 'markdown') && text !== null && (
                <TextViewer
                  key={node.ref}
                  text={text}
                  truncated={truncated}
                  isMarkdown={tier === 'markdown'}
                  ext={node.name.split('.').pop()?.toLowerCase() ?? ''}
                  fontSize={textFontSize}
                />
              )}
            </>
          )}
          {!viewerFailed && source.status === 'idle' && (
            <SystemViewer
              node={node}
              reason={failReason}
              onDownload={() => void handleDownload()}
              onSystemOpen={() => void handleSystemOpen()}
              busy={busy}
            />
          )}

          {/* Edge arrows: hover to reveal, greyed out at the ends
              (no wrap — wrapping would lose the user's place).
              They live inside .preview-content so they track the
              viewing area (not the info panel) when it is open. */}
          <button
          type="button"
          className="preview-nav prev"
          aria-label="上一个文件"
          disabled={!hasPrev}
          onClick={goPrev}
          >
          <IconChevronLeft size={28} />
          </button>
          <button
          type="button"
          className="preview-nav next"
          aria-label="下一个文件"
          disabled={!hasNext}
          onClick={goNext}
          >
          <IconChevronRight size={28} />
          </button>

          {/* Bottom adaptive toolbar: per-type controls, semi-
              transparent, fades out after 2s of mouse stillness. */}
          {tier !== 'system' && tier !== 'video-unplayable' && source.status !== 'error' && (
            <div className={`preview-toolbar${chromeHidden ? ' hidden' : ''}`} role="toolbar" aria-label="预览工具">
              {tier === 'image' && (
                <>
                  <button type="button" className="preview-icon-btn" aria-label="缩小" title="缩小 (-)" onClick={zoomOut}>
                    <IconZoomOut size={18} />
                  </button>
                  <button
                    type="button"
                    className="preview-tb-label"
                    aria-label={imgScale === 'fit' ? '实际大小' : '适应窗口'}
                    title={imgScale === 'fit' ? '实际大小 (100%)' : '适应窗口'}
                    onClick={() => setImgScale((s) => (s === 'fit' ? 1 : 'fit'))}
                  >
                    {imgScale === 'fit' ? '适应' : `${Math.round(imgScale * 100)}%`}
                  </button>
                  <button type="button" className="preview-icon-btn" aria-label="放大" title="放大 (+)" onClick={zoomIn}>
                    <IconZoomIn size={18} />
                  </button>
                  <button type="button" className="preview-icon-btn" aria-label="适应窗口" title="适应窗口" onClick={() => setImgScale('fit')}>
                    <IconExpand size={18} />
                  </button>
                  <button
                    type="button"
                    className="preview-icon-btn"
                    aria-label="旋转"
                    title="旋转 90°"
                    onClick={() => setImgRotate((r) => (r + 90) % 360)}
                  >
                    <IconRotate size={18} />
                  </button>
                </>
              )}
              {tier === 'pdf' && (
                <>
                  <button
                    type="button"
                    className="preview-icon-btn"
                    aria-label="上一页"
                    disabled={!pdfState || pdfState.page <= 1}
                    onClick={() => pdfApiRef.current?.prevPage()}
                  >
                    <IconChevronLeft size={18} />
                  </button>
                  <input
                    className="preview-page-input"
                    type="number"
                    min={1}
                    max={pdfState?.total ?? 1}
                    value={pdfState?.page ?? 1}
                    aria-label="页码"
                    onChange={(e) => {
                      const n = parseInt(e.target.value, 10)
                      if (!Number.isNaN(n)) pdfApiRef.current?.setPage(n)
                    }}
                  />
                  <span className="preview-tb-label">/ {pdfState?.total ?? '—'}</span>
                  <button
                    type="button"
                    className="preview-icon-btn"
                    aria-label="下一页"
                    disabled={!pdfState || pdfState.page >= pdfState.total}
                    onClick={() => pdfApiRef.current?.nextPage()}
                  >
                    <IconChevronRight size={18} />
                  </button>
                  <button type="button" className="preview-icon-btn" aria-label="缩小" title="缩小 (-)" onClick={zoomOut}>
                    <IconZoomOut size={18} />
                  </button>
                  <span className="preview-tb-label">{pdfState ? `${Math.round(pdfState.scale * 100)}%` : '—'}</span>
                  <button type="button" className="preview-icon-btn" aria-label="放大" title="放大 (+)" onClick={zoomIn}>
                    <IconZoomIn size={18} />
                  </button>
                  <button type="button" className="preview-icon-btn" aria-label="适应宽度" title="适应宽度" onClick={() => pdfApiRef.current?.fitWidth()}>
                    <IconExpand size={18} />
                  </button>
                </>
              )}
              {(tier === 'video' || tier === 'audio') && (
                <>
                  <label className="preview-tb-label" htmlFor="preview-speed">
                    倍速
                  </label>
                  <select
                    id="preview-speed"
                    className="preview-speed"
                    value={mediaSpeed}
                    aria-label="播放速度"
                    onChange={(e) => setMediaSpeed(parseFloat(e.target.value))}
                  >
                    {SPEEDS.map((s) => (
                      <option key={s} value={s}>
                        {s}x
                      </option>
                    ))}
                  </select>
                  {tier === 'video' && (
                    <button
                      type="button"
                      className="preview-icon-btn"
                      aria-label="全屏播放"
                      title="全屏播放"
                      onClick={() => {
                        const el = mediaRef.current
                        if (el && 'requestFullscreen' in el) void (el as HTMLVideoElement).requestFullscreen()
                      }}
                    >
                      <IconExpand size={18} />
                    </button>
                  )}
                </>
              )}
              {(tier === 'text' || tier === 'markdown') && (
                <>
                  <button type="button" className="preview-icon-btn" aria-label="减小字号" title="减小字号 (-)" onClick={zoomOut}>
                    <span aria-hidden style={{ fontSize: 13 }}>
                      A−
                    </span>
                  </button>
                  <span className="preview-tb-label">{textFontSize}px</span>
                  <button type="button" className="preview-icon-btn" aria-label="增大字号" title="增大字号 (+)" onClick={zoomIn}>
                    <span aria-hidden style={{ fontSize: 16 }}>
                      A＋
                    </span>
                  </button>
                  <button type="button" className="preview-icon-btn" aria-label="复制全文" title="复制全文" onClick={() => void handleCopyText()}>
                    <IconCopy size={18} />
                  </button>
                </>
              )}
            </div>
          )}
        </div>

        {/* Right info panel (default closed): the same Inspector
            content — basic info, hash, advanced (capabilities,
            versions) — no capacity, ever. The content area narrows
            while it is open. */}
        {infoOpen && (
          <aside className="preview-info" aria-label="文件信息">
            <InspectorBasicInfo node={node} mount={mount} />
            <InspectorAdvanced node={node} mount={mount} onChanged={() => setRetry((r) => r + 1)} />
          </aside>
        )}

      </div>
    </div>
  )
}
