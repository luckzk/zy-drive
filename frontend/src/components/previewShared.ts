// SPDX-License-Identifier: MIT
// previewShared — the one place that decides HOW a file is previewed.
//
// Tier is decided by extension, like the backend's previewMime
// table (internal/app/preview.go). Both the Inspector glance
// (PreviewPane) and the fullscreen overlay (PreviewOverlay) use
// this module, so the two never disagree about which viewer a
// file gets. Text rendering also lives here: Markdown goes
// through marked + DOMPurify (the hand-written renderer is
// retired — it only handled a subset of Markdown and every new
// construct was another special case), code through highlight.js
// with line numbers. All three libraries are lazy-loaded on
// first use so the initial bundle stays lean; the imports are
// cached by the module loader after that.
import { useEffect, useState } from 'react'
import type { Node } from '../mygo'
import { api, friendlyError } from '../api/client'

export type PreviewTier = 'image' | 'video' | 'audio' | 'pdf' | 'text' | 'markdown' | 'video-unplayable' | 'system'

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

export function extOf(name: string): string {
  const i = name.lastIndexOf('.')
  return i < 0 ? '' : name.slice(i + 1).toLowerCase()
}

export function tierOf(node: Node): PreviewTier {
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

export function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

// ---- Markdown (marked + DOMPurify) ----
// marked renders raw HTML from the file as-is, so the output is
// always passed through DOMPurify before it touches the DOM:
// a .md file from a shared folder must never be able to inject
// script into the webview (which has mygo Bind access). Links
// open in a new tab; javascript: hrefs are stripped by the
// sanitizer.
export async function renderMarkdownSafe(src: string): Promise<string> {
  const [{ marked }, { default: DOMPurify }] = await Promise.all([import('marked'), import('dompurify')])
  const dirty = marked.parse(src, { breaks: true })
  const html = typeof dirty === 'string' ? dirty : await dirty
  // Links must open outside the webview: a same-window
  // navigation would replace the whole app. target=_blank is
  // opened by the desktop shell in the system browser (and by
  // window.open in the harness, which the check stubs).
  return DOMPurify.sanitize(html).replace(/<a(\s+href=)/g, '<a target="_blank" rel="noopener noreferrer"$1')
}

// ---- Code highlighting (highlight.js, common languages) ----
// Language by extension; unknown extensions get plain escaped
// text (no highlightAuto — auto-detect over a 512 KiB file is
// slow and usually wrong for config-ish text).
const CODE_LANG: Record<string, string> = {
  go: 'go',
  js: 'javascript',
  jsx: 'javascript',
  ts: 'typescript',
  tsx: 'typescript',
  py: 'python',
  rb: 'ruby',
  rs: 'rust',
  java: 'java',
  c: 'c',
  h: 'c',
  cpp: 'cpp',
  hpp: 'cpp',
  cs: 'csharp',
  sh: 'bash',
  bash: 'bash',
  sql: 'sql',
  html: 'xml',
  css: 'css',
  scss: 'scss',
  json: 'json',
  yaml: 'yaml',
  yml: 'yaml',
  xml: 'xml',
}

export function codeLanguage(ext: string): string | null {
  return CODE_LANG[ext] ?? null
}

export async function highlightCode(code: string, ext: string): Promise<string> {
  const lang = codeLanguage(ext)
  if (!lang) return escapeHtml(code)
  try {
    const hljs = (await import('highlight.js/lib/common')).default
    if (!hljs.getLanguage(lang)) return escapeHtml(code)
    return hljs.highlight(code, { language: lang }).value
  } catch {
    return escapeHtml(code)
  }
}

// ---- Preview source (one fetch per file, shared by glance + overlay) ----
// image/video/audio/pdf go through previewURL (DirectURL first,
// else the zydrive-preview:// proxy); text/markdown through
// previewText (head, at most 512 KiB — the backend truncates).
// system / video-unplayable never fetch: there is nothing to
// render, only the download / system-open buttons.
export type PreviewSource =
  | { status: 'loading' }
  | { status: 'idle' }
  | { status: 'ready'; url?: string; text?: string; truncated?: boolean }
  | { status: 'error'; error: string }

export function usePreviewSource(mountID: string, ref: string, tier: PreviewTier, retry: number): PreviewSource {
  const [source, setSource] = useState<PreviewSource>({ status: 'loading' })
  useEffect(() => {
    let cancelled = false
    if (tier === 'system' || tier === 'video-unplayable') {
      setSource({ status: 'idle' })
      return
    }
    setSource({ status: 'loading' })
    if (tier === 'text' || tier === 'markdown') {
      void api
        .previewText(mountID, ref)
        .then((r) => {
          if (cancelled) return
          setSource({ status: 'ready', text: r.text, truncated: r.truncated })
        })
        .catch((e) => {
          if (cancelled) return
          setSource({ status: 'error', error: friendlyError(e) })
        })
    } else {
      void api
        .previewURL(mountID, ref)
        .then((r) => {
          if (cancelled) return
          setSource({ status: 'ready', url: r.url })
        })
        .catch((e) => {
          if (cancelled) return
          setSource({ status: 'error', error: friendlyError(e) })
        })
    }
    return () => {
      cancelled = true
    }
  }, [mountID, ref, tier, retry])
  return source
}
