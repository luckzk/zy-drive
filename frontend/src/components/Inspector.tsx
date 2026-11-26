// SPDX-License-Identifier: MIT
// Inspector — the right-hand details panel. It only renders when the
// user selected a file or folder (on-demand); otherwise the parent
// does not mount it at all, so the table takes the full width.
// Top: generic info every cloud has. Below: the glance preview,
// then "全屏查看" (opens the fullscreen overlay, v0.14.0), then
// "高级", collapsed by default. Professional items are listed only
// when the mount's Caps says the provider supports them — present
// when supported, hidden otherwise, never greyed-out and dead.
// The info content itself lives in InspectorInfo so the overlay's
// info panel reuses the exact same markup.
//
// v0.13.0: the info is a definition list (.insp-kv in tokens.css,
// Demo v8 .kv — label left, value right, grouped sections with
// dividers), not loose stacked rows. Capacity is deliberately NOT
// shown here — it lives on the sidebar mount card.
import type { Mount, Node } from '../mygo'
import { IconClose } from './icons'
import { InspectorAdvanced, InspectorBasicInfo } from './InspectorInfo'
import PreviewPane from './PreviewPane'

export default function Inspector({
  node,
  mount,
  closing,
  onClose,
  openAdvancedNonce,
  openVersionsNonce,
  onChanged,
  onToast,
  onFullscreen,
}: {
  node: Node
  mount: Mount | null
  closing?: boolean
  onClose: () => void
  openAdvancedNonce?: number
  openVersionsNonce?: number
  onChanged?: () => void
  onToast?: (message: string) => void
  // v0.14.0: open the fullscreen overlay for this file. Files
  // only — folders have no preview.
  onFullscreen?: () => void
}) {
  return (
    <aside
      className={closing ? 'inspector-exit' : 'inspector-enter'}
      style={{ width: 280, flexShrink: 0, borderLeft: '1px solid var(--border)', padding: 16, overflowY: 'auto' }}
    >
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <h2 style={{ margin: 0 }}>详情</h2>
        <button className="icon-btn" onClick={onClose} aria-label="关闭详情">
          <IconClose />
        </button>
      </div>
      <p style={{ margin: '12px 0 0', fontSize: 16, wordBreak: 'break-all' }}>{node.name}</p>

      <InspectorBasicInfo node={node} mount={mount} />

      {/* File preview (v0.9.1): files only, never folders. The
          pane fetches its own URL/text for the selected file and
          re-fetches when the selection changes. */}
      {mount && !node.isDir && <PreviewPane mountID={mount.id} node={node} onToast={onToast} />}

      {/* v0.14.0: the glance's fullscreen entry — the overlay is
          the serious viewer, this pane stays a ~200px glance. */}
      {mount && !node.isDir && onFullscreen && (
        <div style={{ marginTop: 8 }}>
          <button className="btn btn-sm" onClick={onFullscreen}>
            全屏查看
          </button>
        </div>
      )}

      <InspectorAdvanced
        node={node}
        mount={mount}
        openAdvancedNonce={openAdvancedNonce}
        openVersionsNonce={openVersionsNonce}
        onChanged={onChanged}
      />
    </aside>
  )
}
