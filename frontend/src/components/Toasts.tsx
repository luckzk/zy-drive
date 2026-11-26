// SPDX-License-Identifier: MIT
// Toasts — top-right transient notifications (v0.13.0).
//
// One toast auto-dismisses after 5s. Hovering pauses the
// countdown, mouse-leave resumes it (the remaining time is kept,
// not restarted), the X button closes immediately, and several
// toasts stack vertically. The host itself is pointer-events:
// none so toasts never block clicks on the page underneath —
// only the toast cards themselves receive pointer events.
import { useEffect, useRef, useState } from 'react'
import { IconClose } from './icons'

export interface ToastItem {
  id: number
  message: string
}

const TOAST_DURATION_MS = 5000

function Toast({ toast, onDismiss }: { toast: ToastItem; onDismiss: (id: number) => void }) {
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const remainingRef = useRef(TOAST_DURATION_MS)
  const startedAtRef = useRef(0)
  const [paused, setPaused] = useState(false)

  const clearTimer = () => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current)
      timerRef.current = null
    }
  }

  const startTimer = (ms: number) => {
    clearTimer()
    startedAtRef.current = Date.now()
    timerRef.current = setTimeout(() => onDismiss(toast.id), ms)
  }

  useEffect(() => {
    startTimer(remainingRef.current)
    return clearTimer
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const onMouseEnter = () => {
    // Pause: stop the clock and remember what's left.
    remainingRef.current = Math.max(0, remainingRef.current - (Date.now() - startedAtRef.current))
    clearTimer()
    setPaused(true)
  }

  const onMouseLeave = () => {
    // Resume from the remaining time, not from scratch.
    setPaused(false)
    startTimer(remainingRef.current)
  }

  return (
    <div
      className="toast"
      role="status"
      data-paused={paused ? 'true' : undefined}
      onMouseEnter={onMouseEnter}
      onMouseLeave={onMouseLeave}
    >
      <span className="toast-msg">{toast.message}</span>
      <button className="toast-close" type="button" aria-label="关闭通知" onClick={() => onDismiss(toast.id)}>
        <IconClose size={14} />
      </button>
    </div>
  )
}

export default function ToastHost({ toasts, onDismiss }: { toasts: ToastItem[]; onDismiss: (id: number) => void }) {
  if (toasts.length === 0) return null
  return (
    <div className="toast-host" aria-live="polite">
      {toasts.map((t) => (
        <Toast key={t.id} toast={t} onDismiss={onDismiss} />
      ))}
    </div>
  )
}
