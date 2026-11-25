// Motion helpers (ai-design-skills landing-page-design, B7/B9).
// No animation library: CSS transition/animation + IntersectionObserver.
// Rules enforced by tokens.css: base curve cubic-bezier(0.22,1,0.36,1),
// only transform + opacity ever animate, prefers-reduced-motion
// degrades to opacity-only.
import { useEffect, useRef, useState, type ReactNode } from 'react'

// useClosing drives an exit animation before the real close: call
// requestClose() instead of onClose directly; the component stays
// mounted with `closing=true` for `ms` (matching the CSS exit
// duration) and then onClose fires. Unmounting early clears the timer.
// reset() clears a finished closing state: call it when the same
// component reopens (e.g. the transfer panel, the Agent dialog),
// otherwise the next open would render already-closing.
export function useClosing(onClose: () => void, ms = 300): [boolean, () => void, () => void] {
  const [closing, setClosing] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current)
    },
    [],
  )
  const requestClose = () => {
    if (closing) return
    setClosing(true)
    timer.current = setTimeout(onClose, ms)
  }
  const reset = () => {
    if (timer.current) clearTimeout(timer.current)
    setClosing(false)
  }
  return [closing, requestClose, reset]
}

// Reveal fades + rises content the first time it enters the viewport
// (IntersectionObserver, once — never replays on scroll-back, B7).
// Use for below-the-fold blocks; short lists use .stagger-item instead.
export function Reveal({ children, className }: { children: ReactNode; className?: string }) {
  const ref = useRef<HTMLDivElement>(null)
  const [visible, setVisible] = useState(false)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          setVisible(true)
          io.disconnect()
        }
      },
      { threshold: 0.1 },
    )
    io.observe(el)
    return () => io.disconnect()
  }, [])
  return (
    <div ref={ref} className={`reveal${visible ? ' is-visible' : ''}${className ? ` ${className}` : ''}`}>
      {children}
    </div>
  )
}

// Skeleton is a placeholder shaped like the real content (B9: no
// spinner, no bare "loading…" text). Width/height are set by the
// caller to match the row/card it stands in for.
export function Skeleton({ width, height, radius = 8 }: { width?: string | number; height?: string | number; radius?: number }) {
  return (
    <div
      className="skeleton"
      aria-hidden
      style={{ width: width ?? '100%', height: height ?? 14, borderRadius: radius }}
    />
  )
}

// Accordion expands/collapses with grid-template-rows + opacity
// (never height). Content mounts on open and unmounts 500ms after
// close, so the close animation is visible instead of an instant cut.
export function Accordion({ open, children }: { open: boolean; children: ReactNode }) {
  const [render, setRender] = useState(open)
  const [opened, setOpened] = useState(false)
  useEffect(() => {
    if (open) {
      setRender(true)
      const t = setTimeout(() => setOpened(true), 30)
      return () => clearTimeout(t)
    }
    setOpened(false)
    const t = setTimeout(() => setRender(false), 500)
    return () => clearTimeout(t)
  }, [open ])
  if (!render) return null
  return (
    <div className={`collapse${opened ? ' open' : ''}`}>
      <div className="collapse-inner">{children}</div>
    </div>
  )
}

// staggerStyle caps the entrance delay so a list's stagger sequence
// never exceeds ~300ms of delays (60ms per item, B7): the last item
// starts at 300ms and finishes at 700ms with its 400ms rise.
export function staggerStyle(i: number): React.CSSProperties {
  return { '--i': Math.min(i, 5) } as React.CSSProperties
}
