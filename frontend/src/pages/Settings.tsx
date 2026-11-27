// SPDX-License-Identifier: MIT
// Settings — sections: 通用 / 网络 / 网盘 / 同步默认 / 缓存 / Agent / 关于.
// The Agent section: add / edit / delete / test connection /
// set-default, plus the API token field and the encrypted bundle
// export (V0.9.0); the other sections are new in V0.3.
//
// Persistence: one Settings struct in the `settings` table
// (GetSettings/SaveSettings Bind), reloaded on restart. Theme and
// language apply the moment they change (App applies them, this page
// saves); the rest saves per section. Nothing here is faked: the
// mounts list is real ListMounts data, versions come from the
// backend, and update check is honestly marked "not wired yet"
// (no update feed is configured in this repo).
//
// Layout (v0.17.0, Drime 式)：七节排进顶栏 pill 组，一次只显示一节，
// 切 tab 时内容按方向左右滑入/滑出（transform + opacity，350ms，40px，
// 基础曲线；220ms 太快看不到过渡，v0.17.1 放慢加长）。节内行为原样不动；所有 state 都在页面级，切 tab 不
// 卸载任何内容，未保存的编辑原样保留。滚动 Reveal 已去掉——节不再
// 以列表滚动呈现，Reveal 会和滑动打架；行 stagger 在面板内禁用
// （tokens.css），否则每次回切都会重播。加载态是 skeleton 块
// （no "加载中…" text, no spinner）。
import { useCallback, useEffect, useRef, useState } from 'react'
import type { Agent, Mount, MountQuota, ProxyEntry, Settings, SystemProxyInfo, VersionInfo } from '../mygo'
import { api, friendlyError } from '../api/client'
import { formatSize, formatTime, mountStatusLabel, newId } from '../lib/format'
import { t, type Lang } from '../lib/i18n'
import { FRONTEND_VERSION } from '../version'
import { Skeleton, staggerStyle, useClosing } from '../components/motion'
import { SectionIcon } from '../components/icons'

type SettingsSection = 'general' | 'drives' | 'sync' | 'agent' | 'about' | 'network' | 'cache'

// SectionHead — one Settings section's title row: flat-color-icons
// glyph + 18px title (weight stays 400) + one-line description,
// with a divider under it (tokens.css). The title row is the
// hierarchy: icon + size, never bold.
function SectionHead({
  section,
  title,
  desc,
  action,
}: {
  section: SettingsSection
  title: string
  desc: string
  action?: React.ReactNode
}) {
  return (
    <div className="settings-card-head">
      <div className="settings-card-title">
        <SectionIcon section={section} />
        <h2>{title}</h2>
        {action && <span style={{ flex: 1 }} />}
        {action}
      </div>
      <p className="settings-card-desc">{desc}</p>
    </div>
  )
}

// Segmented — Drime 式分段选择（2–4 个互斥选项）：容器是 pill 组，
// 激活段是白色 pill（边框 + 轻阴影），与顶栏 tabs 同一视觉语言。
function Segmented<T extends string>({
  options,
  value,
  onChange,
  ariaLabel,
}: {
  options: { value: T; label: string; disabled?: boolean }[]
  value: T
  onChange: (v: T) => void
  ariaLabel: string
}) {
  return (
    <div className="segmented" role="group" aria-label={ariaLabel}>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          className={`segmented-btn${value === o.value ? ' segmented-btn-active' : ''}`}
          aria-pressed={value === o.value}
          disabled={o.disabled}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

// Switch — Drime 式开关（灰底关 / 品牌绿底开，白圆钮，44×24）。
function Switch({
  checked,
  onChange,
  label,
}: {
  checked: boolean
  onChange: (next: boolean) => void
  label: string
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      className={`switch${checked ? ' switch-on' : ''}`}
      onClick={() => onChange(!checked)}
    >
      <span className="switch-knob" />
    </button>
  )
}

// Row — 一行设置：标题 + 灰色说明在左，控件在右；需要整宽内容
// （输入框等）时放 children，显示在标题行下面。
function Row({
  title,
  desc,
  control,
  children,
}: {
  title: string
  desc?: string
  control?: React.ReactNode
  children?: React.ReactNode
}) {
  return (
    <div className="settings-row">
      <div className="settings-row-head">
        <div className="settings-row-main">
          <div className="settings-row-title">{title}</div>
          {desc && <div className="settings-row-desc">{desc}</div>}
        </div>
        {control && <div className="settings-row-control">{control}</div>}
      </div>
      {children && <div className="settings-row-body">{children}</div>}
    </div>
  )
}

export default function SettingsPage({
  lang,
  theme,
  quotas,
  onThemeChange,
  onLanguageChange,
  onReauth,
  onAddMount,
  onMountsChanged,
}: {
  lang: Lang
  theme: string
  // Capacity per mount (from App's 5-minute refresh): a failed
  // quota carries the plain-language reason, shown under the
  // mount row next to the re-authorize button — that is where
  // the fix (e.g. re-authorize after a missing scope) happens.
  quotas?: Record<string, MountQuota>
  onThemeChange: (theme: string) => void
  onLanguageChange: (lang: Lang) => void
  onReauth: (m: Mount) => void
  onAddMount: () => void
  onMountsChanged: () => void
}) {
  // ---- persisted settings (draft = form state) ----
  const [draft, setDraft] = useState<Settings | null>(null)
  const [settingsLoading, setSettingsLoading] = useState(true)
  const [settingsError, setSettingsError] = useState<string | null>(null)
  const [savedMsg, setSavedMsg] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  // ---- mounts ----
  const [mounts, setMounts] = useState<Mount[]>([])
  const [mountsLoading, setMountsLoading] = useState(true)
  const [mountsError, setMountsError] = useState<string | null>(null)
  const [deletingMountId, setDeletingMountId] = useState<string | null>(null)
  const [reauthTarget, setReauthTarget] = useState<Mount | null>(null)
  // ---- S3 改桶 / 前缀（只改非敏感配置，不碰凭证） ----
  const [editingRoot, setEditingRoot] = useState<Mount | null>(null)
  const [rootBucket, setRootBucket] = useState('')
  const [rootPrefix, setRootPrefix] = useState('')
  const [rootBusy, setRootBusy] = useState(false)
  const [rootError, setRootError] = useState<string | null>(null)
  const [rootDialogClosing, requestRootDialogClose, resetRootDialog] = useClosing(() => setEditingRoot(null))
  // ---- 未验证网盘重新测试（C：一页表单先保存、后测试） ----
  const [testingMountId, setTestingMountId] = useState<string | null>(null)
  const [testMountMsg, setTestMountMsg] = useState<Record<string, string>>({})

  const retestMount = async (m: Mount) => {
    setTestingMountId(m.id)
    try {
      const updated = await api.testMount(m.id)
      if (!updated) throw new Error('重新测试失败：后端没有返回网盘信息。')
      setTestMountMsg((prev) => ({ ...prev, [m.id]: '测试通过，已标记为已验证。' }))
      await loadMounts()
      onMountsChanged()
    } catch (e) {
      setTestMountMsg((prev) => ({ ...prev, [m.id]: friendlyError(e) }))
    } finally {
      setTestingMountId(null)
    }
  }

  const saveRoot = async () => {
    if (!editingRoot) return
    const bucket = rootBucket.trim()
    const prefix = rootPrefix.trim().replace(/^\/+|\/+$/g, '')
    if (!bucket) {
      setRootError('请填桶名（Bucket）。')
      return
    }
    if (bucket.includes('/')) {
      setRootError('桶名里不能有 /：桶名就是一个名字，桶里面的子文件夹请填到“桶内前缀”里。')
      return
    }
    if (prefix === bucket || prefix.startsWith(`${bucket}/`)) {
      setRootError('桶内前缀不要把桶名再填一遍：前缀是桶里面的子文件夹，例如 photos/2024；想挂整个桶就把前缀留空。')
      return
    }
    setRootBusy(true)
    setRootError(null)
    try {
      const updated = await api.updateMountRoot(editingRoot.id, bucket, prefix)
      if (!updated) throw new Error('保存失败：后端没有返回网盘信息。')
      requestRootDialogClose()
      await loadMounts()
      onMountsChanged()
    } catch (e) {
      setRootError(friendlyError(e))
    } finally {
      setRootBusy(false)
    }
  }

  // ---- about ----
  const [versions, setVersions] = useState<VersionInfo | null>(null)

  // ---- open at login (real mygo state; null = unavailable here) ----
  const [openAtLogin, setOpenAtLogin] = useState<boolean | null>(null)
  const [loginNote, setLoginNote] = useState<string | null>(null)

  // ---- download folder check ----
  const [dirMsg, setDirMsg] = useState<string | null>(null)

  // ---- 网络 / 代理 ----
  const [systemProxy, setSystemProxy] = useState<SystemProxyInfo | null>(null)
  // v0.18.0: the test result keeps ok + detail — ok decides the
  // colour (green/red), detail is the backend's own reason.
  const [proxyTestResult, setProxyTestResult] = useState<{ ok: boolean; detail: string } | null>(null)
  const [proxyTestBusy, setProxyTestBusy] = useState(false)
  // v0.18.0: custom test URL (default is the old hardcoded
  // Dropbox token endpoint). Empty = no request, inline hint.
  const [proxyTestUrl, setProxyTestUrl] = useState('https://api.dropboxapi.com/oauth2/token')
  const [proxyTestUrlError, setProxyTestUrlError] = useState<string | null>(null)
  // The proxy fields as last saved: the manual address inputs
  // edit the draft, and "测试代理" only means something once the
  // draft is saved (the backend applies on save, immediately).
  const [lastSavedProxy, setLastSavedProxy] = useState<{ mode: string; http: string; socks5: string } | null>(null)

  // ---- 代理列表（v0.18.0，每盘代理） ----
  // The saved proxy list: each entry can be picked per mount
  // (below, in 网盘） or in the add-mount wizard. Empty list is
  // real — the dropdowns then offer only 跟随全局 / 直连.
  const [proxies, setProxies] = useState<ProxyEntry[]>([])
  const [proxiesLoading, setProxiesLoading] = useState(true)
  const [proxiesError, setProxiesError] = useState<string | null>(null)
  // Inline add/edit form (no dialog): open = shown, id '' = new.
  const [proxyFormOpen, setProxyFormOpen] = useState(false)
  const [proxyFormId, setProxyFormId] = useState('')
  const [proxyFormName, setProxyFormName] = useState('')
  const [proxyFormType, setProxyFormType] = useState('http')
  const [proxyFormAddr, setProxyFormAddr] = useState('')
  const [proxyFormError, setProxyFormError] = useState<string | null>(null)
  const [proxyFormBusy, setProxyFormBusy] = useState(false)
  // Per-row test results (same colour rule as the global test).
  const [proxyRowResults, setProxyRowResults] = useState<Record<string, { ok: boolean; detail: string }>>({})
  const [proxyRowBusy, setProxyRowBusy] = useState<Record<string, boolean>>({})
  // Declarative delete confirm (no static Modal — it does not
  // render under antd 5.29 + React 19; this codebase has no antd
  // at all, so the confirm uses the same .dialog pattern as the
  // other Settings dialogs).
  const [deletingProxy, setDeletingProxy] = useState<ProxyEntry | null>(null)
  const [deleteProxyBusy, setDeleteProxyBusy] = useState(false)
  const [deleteProxyError, setDeleteProxyError] = useState<string | null>(null)
  const [deleteProxyDialogClosing, requestDeleteProxyDialogClose, resetDeleteProxyDialog] = useClosing(() => setDeletingProxy(null))

  // ---- 每盘代理（v0.18.0，网盘行上的下拉框） ----
  // draft holds the just-picked value while the save is in
  // flight; on failure it is dropped, so the select snaps back
  // to the mount's saved proxyRef (the error shows inline).
  const [mountProxyDraft, setMountProxyDraft] = useState<Record<string, string>>({})
  const [mountProxyMsg, setMountProxyMsg] = useState<Record<string, string>>({})
  const [mountProxyBusy, setMountProxyBusy] = useState<Record<string, boolean>>({})

  const loadProxies = useCallback(async () => {
    setProxiesLoading(true)
    try {
      setProxies(await api.listProxies())
      setProxiesError(null)
    } catch (e) {
      setProxiesError(friendlyError(e))
    } finally {
      setProxiesLoading(false)
    }
  }, [])

  // ---- 缓存（v0.9.1 预览缓存） ----
  // cacheInfo is the live size/count/limit from the backend; it is
  // loaded on mount and refreshed after a clear or a limit save.
  // cacheMsg reports the last clear ("已释放 …") or its error.
  const [cacheInfo, setCacheInfo] = useState<import('../mygo').PreviewCacheInfo | null>(null)
  const [cacheLoading, setCacheLoading] = useState(false)
  const [cacheMsg, setCacheMsg] = useState<string | null>(null)
  const [clearingCache, setClearingCache] = useState(false)

  const loadCacheInfo = useCallback(async () => {
    setCacheLoading(true)
    try {
      setCacheInfo(await api.previewCacheInfo())
    } catch {
      setCacheInfo(null)
    } finally {
      setCacheLoading(false)
    }
  }, [])

  const clearCache = async () => {
    setClearingCache(true)
    setCacheMsg(null)
    try {
      const r = await api.clearPreviewCache()
      setCacheMsg(`已清理预览缓存，释放了 ${formatSize(r.freedBytes)}（${r.fileCount} 个文件）。云端有原件，什么都没丢。`)
      await loadCacheInfo()
    } catch (e) {
      setCacheMsg(friendlyError(e))
    } finally {
      setClearingCache(false)
    }
  }

  const loadSystemProxy = useCallback(async () => {
    try {
      setSystemProxy(await api.getSystemProxy())
    } catch {
      setSystemProxy(null)
    }
  }, [])

  const loadSettings = useCallback(async () => {
    setSettingsLoading(true)
    setSettingsError(null)
    try {
      const s = await api.getSettings()
      setDraft(s)
      setLastSavedProxy({ mode: s.proxyMode, http: s.proxyHttp, socks5: s.proxySocks5 })
    } catch (e) {
      setSettingsError(friendlyError(e))
    } finally {
      setSettingsLoading(false)
    }
  }, [])

  const loadMounts = useCallback(async () => {
    setMountsLoading(true)
    setMountsError(null)
    try {
      setMounts(await api.listMounts())
    } catch (e) {
      setMountsError(friendlyError(e))
    } finally {
      setMountsLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadSettings()
    void loadMounts()
    void loadSystemProxy()
    void loadCacheInfo()
    void loadProxies()
    void api
      .getVersions()
      .then(setVersions)
      .catch(() => undefined)
    void api
      .getOpenAtLogin()
      .then((on) => {
        setOpenAtLogin(on)
        setLoginNote(null)
      })
      .catch((e) => {
        // Dev shell / non-packaged run: mygo can't manage login
        // items there. Say so; don't fake a value.
        setOpenAtLogin(null)
        setLoginNote(friendlyError(e))
      })
  }, [loadSettings, loadMounts, loadSystemProxy, loadCacheInfo, loadProxies])

  const saveDraft = async (next: Settings) => {
    setBusy(true)
    setSettingsError(null)
    setSavedMsg(null)
    try {
      await api.saveSettings(next)
      setDraft(next)
      setLastSavedProxy({ mode: next.proxyMode, http: next.proxyHttp, socks5: next.proxySocks5 })
      setSavedMsg(t(lang, 'saved'))
    } catch (e) {
      setSettingsError(friendlyError(e))
    } finally {
      setBusy(false)
    }
  }

  // Proxy mode applies the moment it changes (like theme): the
  // backend applies the saved config immediately, no restart.
  const changeProxyMode = (mode: string) => {
    if (!draft) return
    const s = { ...draft, proxyMode: mode }
    setDraft(s)
    void saveDraft(s)
  }

  // "测试代理": one request through the current (saved) proxy
  // setup. Only meaningful once the draft is saved — the test
  // button stays disabled while the manual addresses are dirty.
  const proxyDirty =
    draft !== null &&
    lastSavedProxy !== null &&
    (draft.proxyMode !== lastSavedProxy.mode || draft.proxyHttp !== lastSavedProxy.http || draft.proxySocks5 !== lastSavedProxy.socks5)

  const runProxyTest = async () => {
    if (!draft || proxyDirty) return
    const url = proxyTestUrl.trim()
    if (!url) {
      setProxyTestUrlError('请填写测试网址')
      return
    }
    setProxyTestUrlError(null)
    setProxyTestBusy(true)
    setProxyTestResult(null)
    try {
      const res = await api.testProxy(url, '')
      setProxyTestResult({ ok: res.ok, detail: res?.detail ?? '无返回' })
    } catch (e) {
      setProxyTestResult({ ok: false, detail: friendlyError(e) })
    } finally {
      setProxyTestBusy(false)
    }
  }

  // Test one list entry against the same URL box (result shows
  // on its own row, coloured the same way). A list entry does
  // not depend on the saved global settings, so it is not gated
  // by proxyDirty — only the global test is.
  const testOneProxy = async (p: ProxyEntry) => {
    const url = proxyTestUrl.trim()
    if (!url) {
      setProxyTestUrlError('请填写测试网址')
      return
    }
    setProxyTestUrlError(null)
    setProxyRowBusy((prev) => ({ ...prev, [p.id]: true }))
    try {
      const res = await api.testProxy(url, p.id)
      setProxyRowResults((prev) => ({ ...prev, [p.id]: { ok: res.ok, detail: res?.detail ?? '无返回' } }))
    } catch (e) {
      setProxyRowResults((prev) => ({ ...prev, [p.id]: { ok: false, detail: friendlyError(e) } }))
    } finally {
      setProxyRowBusy((prev) => ({ ...prev, [p.id]: false }))
    }
  }

  const startAddProxy = () => {
    setProxyFormId('')
    setProxyFormName('')
    setProxyFormType('http')
    setProxyFormAddr('')
    setProxyFormError(null)
    setProxyFormOpen(true)
  }

  const startEditProxy = (p: ProxyEntry) => {
    setProxyFormId(p.id)
    setProxyFormName(p.name)
    setProxyFormType(p.type)
    setProxyFormAddr(p.addr)
    setProxyFormError(null)
    setProxyFormOpen(true)
  }

  const saveProxyForm = async () => {
    const name = proxyFormName.trim()
    const addr = proxyFormAddr.trim()
    if (!name) {
      setProxyFormError('请给代理起个名字。')
      return
    }
    if (!addr) {
      setProxyFormError('请填代理地址，例如 http://127.0.0.1:7890。')
      return
    }
    setProxyFormBusy(true)
    setProxyFormError(null)
    try {
      // Empty id = new entry (the backend mints the id);
      // an existing id updates in place.
      await api.saveProxy({ id: proxyFormId, name, type: proxyFormType, addr })
      setProxyFormOpen(false)
      await loadProxies()
    } catch (e) {
      setProxyFormError(friendlyError(e))
    } finally {
      setProxyFormBusy(false)
    }
  }

  const confirmDeleteProxy = async () => {
    if (!deletingProxy) return
    setDeleteProxyBusy(true)
    setDeleteProxyError(null)
    try {
      await api.deleteProxy(deletingProxy.id)
      requestDeleteProxyDialogClose()
      // Mounts that used it fall back to the global setting;
      // reload both lists so no row keeps pointing at it.
      await loadProxies()
      await loadMounts()
      onMountsChanged()
    } catch (e) {
      setDeleteProxyError(friendlyError(e))
    } finally {
      setDeleteProxyBusy(false)
    }
  }

  // The value the per-mount select shows: a ref to a proxy that
  // no longer exists (deleted) reads as 跟随全局, which is what
  // the backend actually does with it.
  const proxySelectValue = (m: Mount): string => {
    const ref = m.proxyRef ?? ''
    if (ref === '' || ref === 'direct') return ref
    return proxies.some((p) => p.id === ref) ? ref : ''
  }

  const changeMountProxy = async (m: Mount, next: string) => {
    if (next === (m.proxyRef ?? '')) return
    setMountProxyDraft((prev) => ({ ...prev, [m.id]: next }))
    setMountProxyBusy((prev) => ({ ...prev, [m.id]: true }))
    setMountProxyMsg((prev) => {
      const nextMsg = { ...prev }
      delete nextMsg[m.id]
      return nextMsg
    })
    try {
      const updated = await api.updateMountProxy(m.id, next)
      if (!updated) throw new Error('保存失败：后端没有返回网盘信息。')
      setMountProxyDraft((prev) => {
        const nextDraft = { ...prev }
        delete nextDraft[m.id]
        return nextDraft
      })
      await loadMounts()
      onMountsChanged()
    } catch (e) {
      // Drop the draft: the select snaps back to the saved
      // value, and the reason shows inline under the row.
      setMountProxyDraft((prev) => {
        const nextDraft = { ...prev }
        delete nextDraft[m.id]
        return nextDraft
      })
      setMountProxyMsg((prev) => ({ ...prev, [m.id]: friendlyError(e) }))
    } finally {
      setMountProxyBusy((prev) => ({ ...prev, [m.id]: false }))
    }
  }

  // Theme/language: apply now (whole app follows), then persist.
  const changeTheme = (next: string) => {
    if (!draft) return
    const s = { ...draft, theme: next }
    setDraft(s)
    onThemeChange(next)
    void saveDraft(s)
  }

  const changeLanguage = (next: Lang) => {
    if (!draft) return
    const s = { ...draft, language: next }
    setDraft(s)
    onLanguageChange(next)
    void saveDraft(s)
  }

  const checkDir = async () => {
    if (!draft) return
    setDirMsg(null)
    try {
      await api.checkDownloadDir(draft.downloadDir)
      setDirMsg('这个文件夹存在，可以用作默认下载文件夹。')
    } catch (e) {
      setDirMsg(friendlyError(e))
    }
  }

  const toggleLogin = async () => {
    if (openAtLogin === null) return
    const next = !openAtLogin
    setOpenAtLogin(next)
    try {
      await api.setOpenAtLogin(next)
    } catch (e) {
      setOpenAtLogin(!next)
      setLoginNote(friendlyError(e))
    }
  }

  const removeMount = async (m: Mount) => {
    try {
      await api.removeMount(m.id)
      setDeletingMountId(null)
      setReauthTarget((prev) => (prev?.id === m.id ? null : prev))
      await loadMounts()
      onMountsChanged()
    } catch (e) {
      setMountsError(friendlyError(e))
    }
  }

  // ---- Agent (unchanged behaviour, V0.1) ----
  const [agents, setAgents] = useState<Agent[]>([])
  const [agentsLoading, setAgentsLoading] = useState(true)
  const [agentsError, setAgentsError] = useState<string | null>(null)
  const [editing, setEditing] = useState<Agent | null>(null)
  const [formName, setFormName] = useState('')
  const [formAddress, setFormAddress] = useState('')
  const [formToken, setFormToken] = useState('')
  const [deletingId, setDeletingId] = useState<string | null>(null)
  const [testingId, setTestingId] = useState<string | null>(null)
  const [testMsg, setTestMsg] = useState<Record<string, string>>({})
  const [agentBusy, setAgentBusy] = useState(false)
  const [agentDialogClosing, requestAgentDialogClose, resetAgentDialog] = useClosing(() => setEditing(null))
  // 导出配置包 (V0.9.0): per-Agent inline box — passphrase in,
  // file path out. The bundle is encrypted; the user copies it to
  // the server by hand and imports it there.
  const [exportAgentId, setExportAgentId] = useState<string | null>(null)
  const [exportPass, setExportPass] = useState('')
  const [exportBusy, setExportBusy] = useState(false)
  const [exportResult, setExportResult] = useState<import('../mygo').ExportResult | null>(null)
  const [exportError, setExportError] = useState<string | null>(null)

  const loadAgents = useCallback(async () => {
    setAgentsLoading(true)
    setAgentsError(null)
    try {
      setAgents(await api.listAgents())
    } catch (e) {
      setAgentsError(friendlyError(e))
    } finally {
      setAgentsLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadAgents()
  }, [loadAgents])

  const startAdd = () => {
    resetAgentDialog()
    setEditing({
      id: '',
      name: '',
      address: '',
      online: false,
      jobsRunning: 0,
      default: false,
      createdAt: '',
      updatedAt: '',
    })
    setFormName('')
    setFormAddress('')
    setFormToken('')
  }

  const startEdit = (a: Agent) => {
    resetAgentDialog()
    setEditing(a)
    setFormName(a.name)
    setFormAddress(a.address)
    setFormToken('')
  }

  const saveAgentForm = async () => {
    if (!editing) return
    const name = formName.trim()
    const address = formAddress.trim()
    if (!name || !address) {
      setAgentsError('名字和地址都要填。')
      return
    }
    setAgentBusy(true)
    setAgentsError(null)
    try {
      const saved = await api.saveAgent({
        ...editing,
        id: editing.id || newId('agent'),
        name,
        address,
      })
      if (!saved) throw new Error('保存 Agent 失败')
      // Token is only written when the field is non-empty: it is
      // never read back (a secret), so an empty field means
      // "keep the saved one".
      if (formToken.trim()) {
        await api.saveAgentToken(saved.id, formToken.trim())
      }
      requestAgentDialogClose()
      await loadAgents()
    } catch (e) {
      setAgentsError(friendlyError(e))
    } finally {
      setAgentBusy(false)
    }
  }

  const doExportBundle = async (a: Agent) => {
    if (exportPass.trim().length < 8) {
      setExportError('口令至少 8 位：配置包里有网盘凭证，口令太短不安全。')
      return
    }
    setExportBusy(true)
    setExportError(null)
    setExportResult(null)
    try {
      const res = await api.exportAgentBundle(a.id, exportPass.trim())
      if (!res) throw new Error('导出失败：后端没有返回文件路径。')
      setExportResult(res)
    } catch (e) {
      setExportError(friendlyError(e))
    } finally {
      setExportBusy(false)
    }
  }

  const removeAgent = async (a: Agent) => {
    try {
      await api.removeAgent(a.id)
      setDeletingId(null)
      await loadAgents()
    } catch (e) {
      setAgentsError(friendlyError(e))
    }
  }

  const setDefaultAgent = async (id: string) => {
    try {
      await api.setDefaultAgent(id)
      await loadAgents()
    } catch (e) {
      setAgentsError(friendlyError(e))
    }
  }

  const testAgent = async (a: Agent) => {
    setTestingId(a.id)
    try {
      const res = await api.testAgent(a.id)
      setTestMsg((prev) => ({ ...prev, [a.id]: res?.message ?? '无返回' }))
      await loadAgents()
    } catch (e) {
      setTestMsg((prev) => ({ ...prev, [a.id]: friendlyError(e) }))
    } finally {
      setTestingId(null)
    }
  }

  // ---- 顶栏 tabs（Drime 式，v0.17.0） ----
  // 七节按原顺序排进顶栏 pill 组；同一时间只显示一节。切 tab 时
  // 新内容按移动方向滑入（向右切：从右侧进；向左切：从左侧进），
  // 旧内容向反方向滑出；transform + opacity，350ms，40px，基础曲线
  // （220ms 太快看不到过渡，v0.17.1 放慢加长）。
  // 所有 state 都在页面级（draft/mounts/agents/cacheInfo/…），
  // 面板只是 display:none 藏起来、并不卸载，所以切 tab 不丢任何
  // 未保存的编辑。
  const TABS: { id: SettingsSection; label: string }[] = [
    { id: 'general', label: t(lang, 'general') },
    { id: 'network', label: '网络' },
    { id: 'drives', label: t(lang, 'drives') },
    { id: 'sync', label: t(lang, 'syncDefaults') },
    { id: 'cache', label: '缓存' },
    { id: 'agent', label: t(lang, 'agent') },
    { id: 'about', label: t(lang, 'about') },
  ]
  const TAB_ORDER: SettingsSection[] = TABS.map((tb) => tb.id)
  const [activeTab, setActiveTab] = useState<SettingsSection>('general')
  const [leavingTab, setLeavingTab] = useState<SettingsSection | null>(null)
  const [slideDir, setSlideDir] = useState<'left' | 'right'>('right')
  const leaveTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(
    () => () => {
      if (leaveTimer.current) clearTimeout(leaveTimer.current)
    },
    [],
  )
  const switchTab = (to: SettingsSection) => {
    if (to === activeTab) return
    const dir = TAB_ORDER.indexOf(to) > TAB_ORDER.indexOf(activeTab) ? 'right' : 'left'
    if (leaveTimer.current) clearTimeout(leaveTimer.current)
    setLeavingTab(activeTab)
    setSlideDir(dir)
    setActiveTab(to)
    // 390ms 后（动画 350ms + 余量）把滑出的面板藏起来。
    leaveTimer.current = setTimeout(() => setLeavingTab(null), 390)
  }
  const panelClass = (tab: SettingsSection) => {
    if (tab === activeTab) return `settings-panel settings-panel-enter-${slideDir}`
    if (tab === leavingTab) return `settings-panel settings-panel-exit-${slideDir}`
    return 'settings-panel settings-panel-hidden'
  }

  return (
    <div className="settings-page">
      <h1 style={{ marginTop: 0 }}>{t(lang, 'settings')}</h1>
      {(settingsError || agentsError) && <div className="error-box">{settingsError ?? agentsError}</div>}
      {savedMsg && (
        <div className="notice-box" style={{ borderColor: 'var(--brand)' }}>
          {savedMsg}
        </div>
      )}

      {/* 顶栏 tabs（Drime 式 pill 组）：七节按原顺序排；切 tab 只换
          下面一节的内容，节内行为（保存/测试/清理/Agent 操作）原样不动。 */}
      <div className="settings-tabs" role="tablist" aria-label="设置分类">
        {TABS.map((tb) => (
          <button
            key={tb.id}
            type="button"
            role="tab"
            id={`settings-tab-${tb.id}`}
            aria-selected={activeTab === tb.id}
            aria-controls={`settings-panel-${tb.id}`}
            className={`settings-tab${activeTab === tb.id ? ' settings-tab-active' : ''}`}
            onClick={() => switchTab(tb.id)}
          >
            {tb.label}
          </button>
        ))}
      </div>

      <div className="settings-viewport">
      {/* ---- 通用 ---- */}
      <div
        className={panelClass('general')}
        role="tabpanel"
        id="settings-panel-general"
        aria-labelledby="settings-tab-general"
        aria-hidden={activeTab !== 'general'}
      >
        <section className="settings-card">
        <SectionHead section="general" title={t(lang, 'general')} desc="外观、语言、下载文件夹与开机启动。" />
        {settingsLoading || !draft ? (
          <div style={{ display: 'grid', gap: 12 }} aria-label="正在加载设置">
            <Skeleton height={36} width="60%" />
            <Skeleton height={36} width="60%" />
            <Skeleton height={36} width="80%" />
          </div>
        ) : (
          <>
            <Row
              title="主题"
              desc="切换后整个界面立即跟随，不用重启。"
              control={
                <Segmented
                  ariaLabel="主题"
                  value={theme}
                  onChange={(v) => changeTheme(v)}
                  options={[
                    { value: 'light', label: '浅色' },
                    { value: 'dark', label: '深色' },
                  ]}
                />
              }
            />
            <Row
              title="语言 / Language"
              desc="English 翻译目前只覆盖主界面（导航、顶栏、设置页标题与按钮）；文件浏览、同步任务、添加网盘向导等页面仍显示中文，未翻译的文案不假装已翻译。"
              control={
                <Segmented
                  ariaLabel="语言"
                  value={lang}
                  onChange={(v) => changeLanguage(v as Lang)}
                  options={[
                    { value: 'zh', label: '中文' },
                    { value: 'en', label: 'English' },
                  ]}
                />
              }
            />
            <Row title="默认下载文件夹" desc="保存时会校验：路径必须存在且是一个文件夹，否则保存失败。">
              <div style={{ display: 'flex', gap: 8 }}>
                <input
                  className="input"
                  value={draft.downloadDir}
                  onChange={(e) => {
                    setDraft({ ...draft, downloadDir: e.target.value })
                    setDirMsg(null)
                  }}
                  placeholder="例如：/Users/zy/Downloads，留空为不设置"
                />
                <button className="btn" disabled={busy} onClick={() => void checkDir()}>
                  检查
                </button>
              </div>
              {dirMsg && <div className="hint">{dirMsg}</div>}
            </Row>
            {openAtLogin === null ? (
              <Row
                title="开机启动"
                desc={`桌面版暂不支持：${loginNote ?? '当前运行方式下无法管理开机启动，需要打包后的桌面应用（macOS 上是系统设置 → 通用 → 登录项）。'}`}
              />
            ) : (
              <Row
                title="开机启动"
                desc="登录 Mac 时自动启动 ZyDrive。"
                control={<Switch checked={openAtLogin} onChange={() => void toggleLogin()} label="开机启动" />}
              />
            )}
            {loginNote && openAtLogin !== null && <div className="hint">{loginNote}</div>}
            <div style={{ marginBottom: 0, marginTop: 16 }}>
              <button className="btn-primary" disabled={busy} onClick={() => void saveDraft(draft)}>
                {t(lang, 'save')}
              </button>
            </div>
          </>
        )}
        </section>
      </div>

      {/* ---- 网络 ---- */}
      <div
        className={panelClass('network')}
        role="tabpanel"
        id="settings-panel-network"
        aria-labelledby="settings-tab-network"
        aria-hidden={activeTab !== 'network'}
      >
        <section className="settings-card">
        <SectionHead section="network" title="网络" desc="网盘连接走不走代理。保存后立即生效，不用重启。" />
        {settingsLoading || !draft ? (
          <div style={{ display: 'grid', gap: 12 }} aria-label="正在加载设置">
            <Skeleton height={36} width="60%" />
            <Skeleton height={36} width="80%" />
          </div>
        ) : (
          <>
            <Row
              title="代理"
              desc="跟随系统代理：用 macOS 系统设置里的代理（包括 PAC 解析出的第一个代理）；没检测到系统代理时，改读 HTTP_PROXY / HTTPS_PROXY 环境变量。只影响新发起的请求，已在传的文件不受影响。"
              control={
                <Segmented
                  ariaLabel="代理"
                  value={draft.proxyMode === 'manual' ? 'manual' : draft.proxyMode === 'direct' ? 'direct' : 'system'}
                  onChange={(v) => changeProxyMode(v)}
                  options={[
                    { value: 'system', label: '跟随系统代理' },
                    { value: 'manual', label: '手动设置' },
                    { value: 'direct', label: '直连' },
                  ]}
                />
              }
            />
            {systemProxy && (
              <p className="muted" style={{ margin: '0 0 12px' }}>
                {systemProxy.via}
                {systemProxy.pacNote ? ` ${systemProxy.pacNote}` : ''}
              </p>
            )}
            {draft.proxyMode === 'manual' && (
              <>
                <Row title="HTTP 代理（可选）" desc="格式：http://主机:端口 或 https://主机:端口。留空表示 HTTP(S) 请求不走 HTTP 代理。">
                  <input
                    className="input"
                    placeholder="例如：http://127.0.0.1:7890"
                    autoComplete="off"
                    value={draft.proxyHttp}
                    onChange={(e) => setDraft({ ...draft, proxyHttp: e.target.value })}
                  />
                </Row>
                <Row title="SOCKS5 代理（可选）" desc="格式：socks5://主机:端口。只有没填 HTTP 代理时才用 SOCKS5（与 macOS 的处理一致）。">
                  <input
                    className="input"
                    placeholder="例如：socks5://127.0.0.1:1080"
                    autoComplete="off"
                    value={draft.proxySocks5}
                    onChange={(e) => setDraft({ ...draft, proxySocks5: e.target.value })}
                  />
                </Row>
                <div style={{ display: 'flex', gap: 8, marginBottom: 12, flexWrap: 'wrap' }}>
                  <button className="btn-primary" disabled={busy} onClick={() => void saveDraft(draft)}>
                    保存代理设置
                  </button>
                </div>
                {proxyDirty && <p className="muted">地址改了还没保存，先点“保存代理设置”。</p>}
              </>
            )}
            <Row title="测试网址" desc="代理测试向这个网址发一次请求：通了就说明代理能连上网盘服务；不通会写清是代理本身没起来、还是代理连不上对方。">
              <input
                className="input"
                placeholder="https://api.dropboxapi.com/oauth2/token"
                autoComplete="off"
                value={proxyTestUrl}
                onChange={(e) => {
                  setProxyTestUrl(e.target.value)
                  setProxyTestUrlError(null)
                }}
              />
              {proxyTestUrlError && (
                <div className="error-box" style={{ marginTop: 8, marginBottom: 0 }}>
                  {proxyTestUrlError}
                </div>
              )}
            </Row>
            <div style={{ display: 'flex', gap: 8, marginBottom: 12, flexWrap: 'wrap' }}>
              <button className="btn" disabled={proxyTestBusy || proxyDirty} onClick={() => void runProxyTest()}>
                {proxyTestBusy ? '测试中…' : '测试代理'}
              </button>
            </div>
            {proxyTestResult && <div className={proxyTestResult.ok ? 'success-box' : 'error-box'}>{proxyTestResult.detail}</div>}
            <Row title="代理列表" desc="这里的代理可以单独指给某个网盘用（设置 → 网盘里每行都能选；新建网盘时也能选）。删掉一个代理后，用它的网盘会回到跟随全局。">
              {proxiesLoading ? (
                <div style={{ display: 'grid', gap: 8 }} aria-label="正在加载代理列表">
                  <Skeleton height={36} width="80%" />
                </div>
              ) : proxiesError ? (
                <div className="error-box">
                  {proxiesError}
                  <div style={{ marginTop: 8 }}>
                    <button className="btn btn-sm" onClick={() => void loadProxies()}>
                      {t(lang, 'retry')}
                    </button>
                  </div>
                </div>
              ) : proxies.length === 0 ? (
                <p className="muted" style={{ margin: '0 0 12px' }}>还没有保存的代理：点“新增代理”加一个，网盘就能单独走它。</p>
              ) : (
                proxies.map((p) => (
                  <div key={p.id} className="agent-row">
                    <div className="agent-main">
                      <div className="agent-name">
                        {p.name}
                        <span className="badge" style={{ marginLeft: 8 }}>
                          {p.type}
                        </span>
                      </div>
                      <div className="agent-meta">{p.addr}</div>
                      {proxyRowResults[p.id] && (
                        <div
                          className={proxyRowResults[p.id].ok ? 'success-box' : 'error-box'}
                          style={{ marginTop: 8, marginBottom: 0 }}
                        >
                          {proxyRowResults[p.id].detail}
                        </div>
                      )}
                    </div>
                    <button className="btn btn-sm" disabled={proxyRowBusy[p.id]} onClick={() => void testOneProxy(p)}>
                      {proxyRowBusy[p.id] ? '测试中…' : '测试'}
                    </button>
                    <button className="btn btn-sm" onClick={() => startEditProxy(p)}>
                      {t(lang, 'edit')}
                    </button>
                    <button
                      className="btn-danger btn-sm"
                      onClick={() => {
                        resetDeleteProxyDialog()
                        setDeleteProxyError(null)
                        setDeletingProxy(p)
                      }}
                    >
                      {t(lang, 'delete')}
                    </button>
                  </div>
                ))
              )}
              {proxyFormOpen && (
                <div style={{ padding: 12, border: '1px solid var(--border)', borderRadius: 8, marginBottom: 12 }}>
                  <div className="field">
                    <label>名称</label>
                    <input
                      className="input"
                      placeholder="例如：家里的代理"
                      autoComplete="off"
                      value={proxyFormName}
                      onChange={(e) => setProxyFormName(e.target.value)}
                    />
                  </div>
                  <div className="field">
                    <label>类型</label>
                    <select className="select" value={proxyFormType} onChange={(e) => setProxyFormType(e.target.value)}>
                      <option value="http">http</option>
                      <option value="socks5">socks5</option>
                    </select>
                  </div>
                  <div className="field">
                    <label>地址</label>
                    <input
                      className="input"
                      placeholder="例如：http://127.0.0.1:7890"
                      autoComplete="off"
                      value={proxyFormAddr}
                      onChange={(e) => setProxyFormAddr(e.target.value)}
                    />
                  </div>
                  {proxyFormError && <div className="error-box">{proxyFormError}</div>}
                  <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                    <button className="btn-primary btn-sm" disabled={proxyFormBusy} onClick={() => void saveProxyForm()}>
                      {proxyFormBusy ? '保存中…' : '保存'}
                    </button>
                    <button
                      className="btn btn-sm"
                      disabled={proxyFormBusy}
                      onClick={() => {
                        setProxyFormOpen(false)
                        setProxyFormError(null)
                      }}
                    >
                      {t(lang, 'cancel')}
                    </button>
                  </div>
                </div>
              )}
              {!proxyFormOpen && (
                <button className="btn" onClick={startAddProxy}>
                  新增代理
                </button>
              )}
            </Row>
          </>
        )}
        </section>
      </div>

      {/* ---- 网盘 ---- */}
      <div
        className={panelClass('drives')}
        role="tabpanel"
        id="settings-panel-drives"
        aria-labelledby="settings-tab-drives"
        aria-hidden={activeTab !== 'drives'}
      >
        <section className="settings-card">
        <SectionHead section="drives" title={t(lang, 'drives')} desc="已添加的网盘：查看状态、重新授权、重新测试未验证的，或移除。" />
        {mountsLoading ? (
          <div style={{ display: 'grid', gap: 8 }} aria-label="正在加载网盘">
            {[0, 1].map((i) => (
              <div key={i} className="agent-row">
                <div className="agent-main">
                  <Skeleton height={14} width={140} />
                  <div style={{ marginTop: 6 }}>
                    <Skeleton height={12} width={220} />
                  </div>
                </div>
              </div>
            ))}
          </div>
        ) : mountsError ? (
          <div className="error-box">
            {mountsError}
            <div style={{ marginTop: 8 }}>
              <button className="btn btn-sm" onClick={() => void loadMounts()}>
                {t(lang, 'retry')}
              </button>
            </div>
          </div>
        ) : mounts.length === 0 ? (
          <div className="empty">
            <h2>还没有添加网盘</h2>
            <p>添加一个网盘后，就能在这里浏览、重新授权或移除它。</p>
            <button className="btn-primary" onClick={onAddMount}>
              添加网盘
            </button>
          </div>
        ) : (
          mounts.map((m, i) => (
            <div key={m.id} className="agent-row stagger-item" style={staggerStyle(i)}>
              <div className="agent-main">
                <div className="agent-name">
                  <span className={m.status === 'ok' ? 'dot dot-green' : 'dot dot-red'} />
                  {m.displayName}
                  {m.status === 'auth_expired' && (
                    <span className="badge badge-amber" style={{ marginLeft: 8 }}>
                      授权过期
                    </span>
                  )}
                  {m.status === 'error' && (
                    <span className="badge badge-red" style={{ marginLeft: 8 }}>
                      异常
                    </span>
                  )}
                  {!m.verified && (
                    <span className="badge badge-amber" style={{ marginLeft: 8 }}>
                      未验证
                    </span>
                  )}
                </div>
                <div className="agent-meta">
                  {m.provider}
                  {m.group ? ` · 分组“${m.group}”` : ''}
                  {mountStatusLabel(m.status) ? ` · ${mountStatusLabel(m.status)}` : ''}
                  {m.rootLabel || m.rootRef ? ` · ${m.rootLabel || m.rootRef}` : ''}
                  {m.workspaceLabel ? ` · ${m.workspaceLabel}` : ''}
                </div>
                {testMountMsg[m.id] && (
                  <div style={{ fontSize: 13, marginTop: 4 }}>
                    {testMountMsg[m.id]}
                  </div>
                )}
                {quotas?.[m.id]?.error && (
                  <div style={{ fontSize: 13, marginTop: 4 }}>
                    容量暂不可用：{quotas[m.id].error}
                  </div>
                )}
                {m.loadError && (
                  <div style={{ fontSize: 13, marginTop: 4, color: 'var(--danger)' }}>
                    启动加载失败：{m.loadError}
                  </div>
                )}
                <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginTop: 8, flexWrap: 'wrap' }}>
                  <label htmlFor={`mount-proxy-${m.id}`} style={{ fontSize: 13 }}>
                    代理
                  </label>
                  <select
                    id={`mount-proxy-${m.id}`}
                    className="select"
                    style={{ minWidth: 140 }}
                    disabled={mountProxyBusy[m.id]}
                    value={mountProxyDraft[m.id] ?? proxySelectValue(m)}
                    onChange={(e) => void changeMountProxy(m, e.target.value)}
                  >
                    <option value="">跟随全局</option>
                    <option value="direct">直连</option>
                    {proxies.map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name}
                      </option>
                    ))}
                  </select>
                  {mountProxyBusy[m.id] && (
                    <span className="muted" style={{ fontSize: 13 }}>
                      保存中…
                    </span>
                  )}
                </div>
                {mountProxyMsg[m.id] && (
                  <div style={{ fontSize: 13, marginTop: 4, color: 'var(--danger)' }}>
                    {mountProxyMsg[m.id]}
                  </div>
                )}
                {reauthTarget?.id === m.id && (
                  <div style={{ fontSize: 13, marginTop: 4 }}>
                    正在重新授权：走完后还是这个网盘，同步任务、分组和缓存都在，不会新建一个盘。
                  </div>
                )}
              </div>
              {(!m.verified || m.loadError) && (
                <button className="btn btn-sm" disabled={testingMountId === m.id} onClick={() => void retestMount(m)}>
                  {testingMountId === m.id ? '测试中…' : '重新测试'}
                </button>
              )}
              <button
                className="btn btn-sm"
                onClick={() => {
                  setReauthTarget(m)
                  onReauth(m)
                }}
              >
                重新授权
              </button>
              {m.provider === 's3' && (
                <button
                  className="btn btn-sm"
                  onClick={() => {
                    // 当前桶/前缀从显示用的 rootLabel 里拆出来
                    // （"bucket" 或 "bucket/prefix"）；凭证不动。
                    const label = m.rootLabel || m.rootRef || ''
                    const slash = label.indexOf('/')
                    resetRootDialog()
                    setEditingRoot(m)
                    setRootBucket(slash === -1 ? label : label.slice(0, slash))
                    setRootPrefix(slash === -1 ? '' : label.slice(slash + 1))
                    setRootError(null)
                  }}
                >
                  修改桶 / 前缀
                </button>
              )}
              {deletingMountId === m.id ? (
                <>
                  <button className="btn-danger btn-sm" onClick={() => void removeMount(m)}>
                    确认删除？
                  </button>
                  <button className="btn btn-sm" onClick={() => setDeletingMountId(null)}>
                    {t(lang, 'cancel')}
                  </button>
                </>
              ) : (
                <button className="btn-danger btn-sm" onClick={() => setDeletingMountId(m.id)}>
                  {t(lang, 'delete')}
                </button>
              )}
            </div>
          ))
        )}
        {mounts.length > 0 && (
          <p className="muted" style={{ fontSize: 13, margin: '12px 0 0' }}>
            删除网盘只删本机的记录和保存的凭证，不会删除云端 / 本地文件夹里的文件。
          </p>
        )}
        </section>
      </div>

      {/* ---- 同步默认 ---- */}
      <div
        className={panelClass('sync')}
        role="tabpanel"
        id="settings-panel-sync"
        aria-labelledby="settings-tab-sync"
        aria-hidden={activeTab !== 'sync'}
      >
        <section className="settings-card">
        <SectionHead section="sync" title={t(lang, 'syncDefaults')} desc="新建同步任务时用这些默认值；已经建好的任务不受影响，各自保留原来的设置。" />
        {settingsLoading || !draft ? (
          <div style={{ display: 'grid', gap: 12 }} aria-label="正在加载同步默认设置">
            <Skeleton height={36} width="60%" />
            <Skeleton height={36} width="40%" />
            <Skeleton height={36} width="40%" />
          </div>
        ) : (
          <>
            <Row
              title="默认比较方式"
              control={
                <Segmented
                  ariaLabel="默认比较方式"
                  value={draft.syncCompare}
                  onChange={(v) => setDraft({ ...draft, syncCompare: v as Settings['syncCompare'] })}
                  options={[
                    { value: 'size+time', label: '大小 + 修改时间' },
                    { value: 'size', label: '仅大小' },
                    { value: 'checksum', label: '校验值' },
                  ]}
                />
              }
            />
            <Row
              title="冲突处理默认策略"
              desc="V0.1 引擎的冲突规则是固定的（新版占原名、另一份保留为冲突副本），其他策略后续版本再提供。"
              control={
                <Segmented
                  ariaLabel="冲突处理默认策略"
                  value={draft.syncConflict}
                  onChange={(v) => setDraft({ ...draft, syncConflict: v })}
                  options={[
                    { value: 'keep-both', label: '保留双方' },
                    { value: 'ask', label: '每次询问', disabled: true },
                    { value: 'newer-wins', label: '直接覆盖', disabled: true },
                  ]}
                />
              }
            />
            <Row
              title="默认并发数（1–16）"
              desc="同步时同时传几个文件（默认 4）。Drime 遇到 429 可能罚 15 小时，并发别乱调高；已建任务的并发在下次运行时取最新设置。"
              control={
                <input
                  className="input"
                  type="number"
                  min={1}
                  max={16}
                  style={{ width: 120 }}
                  value={draft.syncConcurrency}
                  onChange={(e) => setDraft({ ...draft, syncConcurrency: Number(e.target.value) })}
                />
              }
            />
            <Row
              title="限速（KB/s，0 = 不限速）"
              desc="全局上限：所有传输加起来不超过这个速度（不是每个文件一路）。0 = 不限速。"
              control={
                <input
                  className="input"
                  type="number"
                  min={0}
                  style={{ width: 120 }}
                  value={draft.syncSpeedLimitKBps}
                  onChange={(e) => setDraft({ ...draft, syncSpeedLimitKBps: Number(e.target.value) })}
                />
              }
            />
            <div style={{ marginBottom: 0 }}>
              <button className="btn-primary" disabled={busy} onClick={() => void saveDraft(draft)}>
                {t(lang, 'save')}
              </button>
            </div>
          </>
        )}
        </section>
      </div>

      {/* ---- 缓存（v0.9.1 预览缓存） ---- */}
      <div
        className={panelClass('cache')}
        role="tabpanel"
        id="settings-panel-cache"
        aria-labelledby="settings-tab-cache"
        aria-hidden={activeTab !== 'cache'}
      >
        <section className="settings-card">
        <SectionHead section="cache" title="缓存" desc="预览缓存：用系统应用打开文件时下载下来的副本。云端有原件，清理不会丢东西；应用内直接预览不进缓存。" />
        {settingsLoading || !draft ? (
          <div style={{ display: 'grid', gap: 12 }} aria-label="正在加载缓存设置">
            <Skeleton height={36} width="60%" />
            <Skeleton height={36} width="40%" />
          </div>
        ) : (
          <>
            <Row
              title="当前占用"
              desc={
                cacheLoading
                  ? '正在读取缓存大小…'
                  : cacheInfo
                    ? `${formatSize(cacheInfo.sizeBytes)}（${cacheInfo.fileCount} 个文件），上限 ${cacheInfo.limitMB >= 1024 ? `${cacheInfo.limitMB / 1024} GB` : `${cacheInfo.limitMB} MB`}。`
                    : '缓存信息暂不可用（后端没返回）。'
              }
            />
            <Row
              title="缓存上限"
              desc="超上限时按最后访问时间淘汰最久没用的文件；最近 5 分钟内打开过的文件不会被淘汰。改小上限保存后立即裁剪。"
              control={
                <Segmented
                  ariaLabel="缓存上限"
                  value={String(draft.previewCacheLimitMB)}
                  onChange={(v) => setDraft({ ...draft, previewCacheLimitMB: Number(v) })}
                  options={[
                    { value: '512', label: '500 MB' },
                    { value: '1024', label: '1 GB' },
                    { value: '2048', label: '2 GB' },
                    { value: '5120', label: '5 GB' },
                  ]}
                />
              }
            />
            <Row
              title="退出时清理缓存"
              control={
                <Switch
                  checked={draft.previewCacheClearOnExit}
                  onChange={(next) => setDraft({ ...draft, previewCacheClearOnExit: next })}
                  label="退出时清理缓存"
                />
              }
            />
            <div style={{ display: 'flex', gap: 8, marginBottom: 12, flexWrap: 'wrap' }}>
              <button
                className="btn-primary"
                disabled={busy}
                onClick={() => {
                  void saveDraft(draft).then(() => void loadCacheInfo())
                }}
              >
                {t(lang, 'save')}
              </button>
              <button className="btn" disabled={clearingCache} onClick={() => void clearCache()}>
                {clearingCache ? '清理中…' : '清理缓存'}
              </button>
            </div>
            {cacheMsg && <div className="notice-box">{cacheMsg}</div>}
            <p className="muted" style={{ marginBottom: 0 }}>
              “清理缓存”只清预览缓存，不碰挂载、同步任务和本地索引。
            </p>
          </>
        )}
        </section>
      </div>

      {/* ---- Agent（V0.1 原样保留） ---- */}
      <div
        className={panelClass('agent')}
        role="tabpanel"
        id="settings-panel-agent"
        aria-labelledby="settings-tab-agent"
        aria-hidden={activeTab !== 'agent'}
      >
        <section className="settings-card">
        <SectionHead
          section="agent"
          title={t(lang, 'agent')}
          desc="Agent 是跑在服务器上的执行端：桌面配好同步任务后，Agent 在服务器上跑，文件不经过这台电脑。本地文件夹的任务只能在本机跑。"
          action={
            <button className="btn-primary" onClick={startAdd}>
              添加 Agent
            </button>
          }
        />

        {agentsLoading ? (
          <div style={{ display: 'grid', gap: 8 }} aria-label="正在加载 Agent">
            {[0, 1].map((i) => (
              <div key={i} className="agent-row">
                <div className="agent-main">
                  <Skeleton height={14} width={160} />
                  <div style={{ marginTop: 6 }}>
                    <Skeleton height={12} width={260} />
                  </div>
                </div>
                <Skeleton height={28} width={72} />
              </div>
            ))}
          </div>
        ) : agents.length === 0 ? (
          <div className="empty">
            <h2>还没有配置 Agent</h2>
            <p>没有 Agent 也能用：同步任务在本机跑。想让任务在服务器上跑，先在服务器上起一个 zy-drive-agent，再点“添加 Agent”。</p>
          </div>
        ) : (
          agents.map((a, i) => (
            <div key={a.id} className="agent-row stagger-item" style={staggerStyle(i)}>
              <div className="agent-main">
                <div className="agent-name">
                  <span className={a.online ? 'dot dot-green' : 'dot dot-grey'} />
                  {a.name}
                  {a.default && (
                    <span className="badge badge-green" style={{ marginLeft: 8 }}>
                      默认
                    </span>
                  )}
                  {!a.online && (
                    <span className="badge" style={{ marginLeft: 8 }}>
                      离线
                    </span>
                  )}
                </div>
                <div className="agent-meta">
                  {a.address}
                  {a.version ? ` · 版本 ${a.version}` : ''}
                  {a.protocolVersion ? ` · 协议 ${a.protocolVersion}` : ''}
                  {` · 运行中 ${a.jobsRunning} 个任务`}
                  {a.lastHeartbeat ? ` · 上次心跳 ${formatTime(a.lastHeartbeat)}` : ' · 还没有心跳'}
                </div>
                {testMsg[a.id] && <div style={{ fontSize: 13, marginTop: 4 }}>{testMsg[a.id]}</div>}
                {exportAgentId === a.id && (
                  <div style={{ marginTop: 8, padding: 12, border: '1px solid var(--border)', borderRadius: 8 }}>
                    <div style={{ fontSize: 13, marginBottom: 8 }}>
                      导出加密配置包（该 Agent 的任务 + 用到的网盘凭证），自己拷到服务器上导入。凭证只走你自己的手，不经过我们的服务器。
                    </div>
                    <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
                      <input
                        className="input"
                        type="password"
                        autoComplete="off"
                        style={{ width: 200 }}
                        placeholder="配置包口令（至少 8 位）"
                        value={exportPass}
                        onChange={(e) => setExportPass(e.target.value)}
                      />
                      <button className="btn-primary btn-sm" disabled={exportBusy} onClick={() => void doExportBundle(a)}>
                        {exportBusy ? '导出中…' : '导出'}
                      </button>
                    </div>
                    {exportError && <div className="error-box" style={{ marginTop: 8 }}>{exportError}</div>}
                    {exportResult && (
                      <div className="notice-box" style={{ marginTop: 8, marginBottom: 0 }}>
                        <div style={{ fontSize: 13 }}>已导出到：</div>
                        <div style={{ fontSize: 13, wordBreak: 'break-all' }}>{exportResult.path}</div>
                        <div style={{ fontSize: 12, marginTop: 4 }} className="muted">
                          把这个文件拷到服务器，跑 <code>zy-drive-agent import --file &lt;文件&gt;</code>（口令交互输入或经 ZY_DRIVE_BUNDLE_PASSPHRASE），再启动 Agent。
                        </div>
                        {exportResult.skipped && exportResult.skipped.length > 0 && (
                          <div style={{ fontSize: 12, marginTop: 4 }}>
                            跳过的任务（服务器跑不了，仍留在本机）：
                            <ul style={{ margin: '4px 0 0', paddingLeft: 16 }}>
                              {exportResult.skipped.map((s) => (
                                <li key={s}>{s}</li>
                              ))}
                            </ul>
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                )}
              </div>
              <button className="btn btn-sm" disabled={testingId === a.id} onClick={() => void testAgent(a)}>
                {testingId === a.id ? '测试中…' : '测试连接'}
              </button>
              <button
                className="btn btn-sm"
                onClick={() => {
                  const open = exportAgentId !== a.id
                  setExportAgentId(open ? a.id : null)
                  setExportPass('')
                  setExportResult(null)
                  setExportError(null)
                }}
              >
                {exportAgentId === a.id ? '收起导出' : '导出配置包'}
              </button>
              {!a.default && (
                <button className="btn btn-sm" onClick={() => void setDefaultAgent(a.id)}>
                  设为默认
                </button>
              )}
              <button className="btn btn-sm" onClick={() => startEdit(a)}>
                {t(lang, 'edit')}
              </button>
              {deletingId === a.id ? (
                <>
                  <button className="btn-danger btn-sm" onClick={() => void removeAgent(a)}>
                    {t(lang, 'confirmDelete')}
                  </button>
                  <button className="btn btn-sm" onClick={() => setDeletingId(null)}>
                    {t(lang, 'cancel')}
                  </button>
                </>
              ) : (
                <button className="btn-danger btn-sm" onClick={() => setDeletingId(a.id)}>
                  {t(lang, 'delete')}
                </button>
              )}
            </div>
          ))
        )}
        </section>
      </div>

      {/* ---- 关于 ---- */}
      <div
        className={panelClass('about')}
        role="tabpanel"
        id="settings-panel-about"
        aria-labelledby="settings-tab-about"
        aria-hidden={activeTab !== 'about'}
      >
        <section className="settings-card">
        <SectionHead section="about" title={t(lang, 'about')} desc="版本信息。" />
        <Row title="后端版本" control={<span>{versions?.backend ?? '—'}</span>} />
        <Row title="前端版本" control={<span>{FRONTEND_VERSION}</span>} />
        <Row
          title="协议版本"
          desc={versions?.engine ? `同步引擎 ${versions.engine}` : undefined}
          control={<span>{versions?.protocol ?? '—'}</span>}
        />
        <div className="notice-box" style={{ marginBottom: 0 }}>暂未接入检查更新：还没有配置更新源（mygo Updater 需要打包时配置更新 feed），所以这里不假装检查过。当前版本见上方。</div>
        </section>
      </div>
      </div>

      {editingRoot && (
        <div className={`dialog-backdrop${rootDialogClosing ? ' closing' : ''}`} onClick={requestRootDialogClose}>
          <div className={`dialog${rootDialogClosing ? ' closing' : ''}`} onClick={(e) => e.stopPropagation()}>
            <h1>修改桶 / 前缀</h1>
            <p className="muted" style={{ margin: '0 0 12px' }}>
              改“{editingRoot.displayName}”挂载的桶和范围。只改桶和前缀，不碰 AccessKey / SecretKey；保存前会先连一次新桶根目录，通了才保存，不通不改。
            </p>
            <div className="field">
              <label>桶名（Bucket）</label>
              <input className="input" value={rootBucket} onChange={(e) => setRootBucket(e.target.value)} placeholder="例如：my-backup" />
            </div>
            <div className="field">
              <label>桶内前缀（可选）</label>
              <input className="input" value={rootPrefix} onChange={(e) => setRootPrefix(e.target.value)} placeholder="例如：photos/2024" />
              <div className="hint">留空 = 整个桶。只填桶里面的子文件夹，例如 photos/2024，不要把桶名再填一遍。</div>
            </div>
            {rootError && <div className="error-box">{rootError}</div>}
            <div className="dialog-actions">
              <button className="btn" disabled={rootBusy} onClick={requestRootDialogClose}>
                {t(lang, 'cancel')}
              </button>
              <button className="btn-primary" disabled={rootBusy} onClick={() => void saveRoot()}>
                {rootBusy ? '测试中…' : '测试并保存'}
              </button>
            </div>
          </div>
        </div>
      )}

      {deletingProxy && (
        <div className={`dialog-backdrop${deleteProxyDialogClosing ? ' closing' : ''}`} onClick={requestDeleteProxyDialogClose}>
          <div className={`dialog${deleteProxyDialogClosing ? ' closing' : ''}`} onClick={(e) => e.stopPropagation()}>
            <h1>删除代理“{deletingProxy.name}”？</h1>
            <p className="muted" style={{ margin: '0 0 12px' }}>
              删掉后，用这个代理的网盘会回到跟随全局，不会删网盘、也不会动云端文件。
            </p>
            {deleteProxyError && <div className="error-box">{deleteProxyError}</div>}
            <div className="dialog-actions">
              <button className="btn" disabled={deleteProxyBusy} onClick={requestDeleteProxyDialogClose}>
                {t(lang, 'cancel')}
              </button>
              <button className="btn-danger" disabled={deleteProxyBusy} onClick={() => void confirmDeleteProxy()}>
                {deleteProxyBusy ? '删除中…' : '确认删除'}
              </button>
            </div>
          </div>
        </div>
      )}

      {editing && (
        <div className={`dialog-backdrop${agentDialogClosing ? ' closing' : ''}`} onClick={requestAgentDialogClose}>
          <div className={`dialog${agentDialogClosing ? ' closing' : ''}`} onClick={(e) => e.stopPropagation()}>
            <h1>{editing.id ? '编辑 Agent' : '添加 Agent'}</h1>
            <div className="field">
              <label>名字</label>
              <input className="input" value={formName} onChange={(e) => setFormName(e.target.value)} placeholder="例如：家里的服务器" />
            </div>
            <div className="field">
              <label>地址</label>
              <input
                className="input"
                value={formAddress}
                onChange={(e) => setFormAddress(e.target.value)}
                placeholder="例如：192.168.1.10:8080 或 agent.example.com"
              />
              <div className="hint">填 Agent 的 HTTP 地址，不用加 http:// 也行。测试连接调的是这个地址的 /api/health，不需要 Token。</div>
            </div>
            <div className="field">
              <label>API Token（Agent 启动时配的 ZY_DRIVE_AGENT_TOKEN）</label>
              <input
                className="input"
                type="password"
                autoComplete="off"
                value={formToken}
                onChange={(e) => setFormToken(e.target.value)}
                placeholder="留空 = 不改已保存的令牌"
              />
              <div className="hint">推送任务、立即同步、拉运行记录都要用它。令牌存在本机、加密保存；服务器上改了令牌后，这里也要改成一样的。</div>
            </div>
            <div className="notice-box">
              定时任务在 Agent 上跑（同步任务页里设定时、点“立即同步”或等定时触发）。凭证不随推送走：先在设置 → Agent 里点“导出配置包”，把加密包拷到服务器上导入，Agent 才能跑这些任务。
            </div>
            <div className="dialog-actions">
              <button className="btn" onClick={requestAgentDialogClose}>
                {t(lang, 'cancel')}
              </button>
              <button className="btn-primary" disabled={agentBusy} onClick={() => void saveAgentForm()}>
                {t(lang, 'save')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
