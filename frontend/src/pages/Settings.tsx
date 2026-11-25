// Settings — sections: 通用 / 网盘 / 同步默认 / Agent / 关于.
// The Agent section is kept as-is (add / edit / delete / test
// connection / set-default); the other sections are new in V0.3.
//
// Persistence: one Settings struct in the `settings` table
// (GetSettings/SaveSettings Bind), reloaded on restart. Theme and
// language apply the moment they change (App applies them, this page
// saves); the rest saves per section. Nothing here is faked: the
// mounts list is real ListMounts data, versions come from the
// backend, and update check is honestly marked "not wired yet"
// (no update feed is configured in this repo).
//
// Motion (ai-design-skills, B7/B9): sections Reveal once on scroll
// into view, rows stagger in, loading is skeleton blocks shaped like
// the real rows (no "加载中…" text, no spinner).
import { useCallback, useEffect, useState } from 'react'
import type { Agent, Mount, MountQuota, Settings, SystemProxyInfo, VersionInfo } from '../mygo'
import { api, friendlyError } from '../api/client'
import { formatTime, mountStatusLabel, newId } from '../lib/format'
import { t, type Lang } from '../lib/i18n'
import { FRONTEND_VERSION } from '../version'
import { Reveal, Skeleton, staggerStyle, useClosing } from '../components/motion'
import { SectionIcon } from '../components/icons'

type SettingsSection = 'general' | 'drives' | 'sync' | 'agent' | 'about' | 'network'

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
  const [proxyTestResult, setProxyTestResult] = useState<string | null>(null)
  const [proxyTestBusy, setProxyTestBusy] = useState(false)
  // The proxy fields as last saved: the manual address inputs
  // edit the draft, and "测试代理" only means something once the
  // draft is saved (the backend applies on save, immediately).
  const [lastSavedProxy, setLastSavedProxy] = useState<{ mode: string; http: string; socks5: string } | null>(null)

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
  }, [loadSettings, loadMounts, loadSystemProxy])

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
    setProxyTestBusy(true)
    setProxyTestResult(null)
    try {
      const res = await api.testProxy()
      setProxyTestResult(res?.detail ?? '无返回')
    } catch (e) {
      setProxyTestResult(friendlyError(e))
    } finally {
      setProxyTestBusy(false)
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
  const [deletingId, setDeletingId] = useState<string | null>(null)
  const [testingId, setTestingId] = useState<string | null>(null)
  const [testMsg, setTestMsg] = useState<Record<string, string>>({})
  const [agentBusy, setAgentBusy] = useState(false)
  const [agentDialogClosing, requestAgentDialogClose, resetAgentDialog] = useClosing(() => setEditing(null))

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
  }

  const startEdit = (a: Agent) => {
    resetAgentDialog()
    setEditing(a)
    setFormName(a.name)
    setFormAddress(a.address)
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
      requestAgentDialogClose()
      await loadAgents()
    } catch (e) {
      setAgentsError(friendlyError(e))
    } finally {
      setAgentBusy(false)
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

  return (
    <div className="settings-page">
      <h1 style={{ marginTop: 0 }}>{t(lang, 'settings')}</h1>
      {(settingsError || agentsError) && <div className="error-box">{settingsError ?? agentsError}</div>}
      {savedMsg && (
        <div className="notice-box" style={{ borderColor: 'var(--brand)' }}>
          {savedMsg}
        </div>
      )}

      {/* ---- 通用 ---- */}
      <Reveal>
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
            <div className="field">
              <label>主题</label>
              <select className="select" value={theme} onChange={(e) => changeTheme(e.target.value)}>
                <option value="light">浅色</option>
                <option value="dark">深色</option>
              </select>
              <div className="hint">切换后整个界面立即跟随，不用重启。</div>
            </div>
            <div className="field">
              <label>语言 / Language</label>
              <select className="select" value={lang} onChange={(e) => changeLanguage(e.target.value as Lang)}>
                <option value="zh">中文</option>
                <option value="en">English</option>
              </select>
              <div className="hint">
                English 翻译目前只覆盖主界面（导航、顶栏、设置页标题与按钮）；文件浏览、同步任务、添加网盘向导等页面仍显示中文，未翻译的文案不假装已翻译。
              </div>
            </div>
            <div className="field">
              <label>默认下载文件夹</label>
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
              <div className="hint">保存时会校验：路径必须存在且是一个文件夹，否则保存失败。</div>
              {dirMsg && <div className="hint">{dirMsg}</div>}
            </div>
            <details className="settings-advanced">
              <summary>高级：开机启动</summary>
              <div className="field" style={{ marginTop: 12 }}>
                <label>开机启动</label>
                {openAtLogin === null ? (
                  <p className="muted" style={{ margin: '4px 0' }}>
                    桌面版暂不支持：{loginNote ?? '当前运行方式下无法管理开机启动，需要打包后的桌面应用（macOS 上是系统设置 → 通用 → 登录项）。'}
                  </p>
                ) : (
                  <label className="radio-row">
                    <input type="checkbox" checked={openAtLogin} onChange={() => void toggleLogin()} />
                    <span>登录 Mac 时自动启动 ZyDrive{openAtLogin ? '（已开启）' : '（未开启）'}</span>
                  </label>
                )}
                {loginNote && openAtLogin !== null && <div className="hint">{loginNote}</div>}
              </div>
            </details>
            <div style={{ marginBottom: 0, marginTop: 16 }}>
              <button className="btn-primary" disabled={busy} onClick={() => void saveDraft(draft)}>
                {t(lang, 'save')}
              </button>
            </div>
          </>
        )}
        </section>
      </Reveal>

      {/* ---- 网络 ---- */}
      <Reveal>
        <section className="settings-card">
        <SectionHead section="network" title="网络" desc="网盘连接走不走代理。保存后立即生效，不用重启。" />
        {settingsLoading || !draft ? (
          <div style={{ display: 'grid', gap: 12 }} aria-label="正在加载设置">
            <Skeleton height={36} width="60%" />
            <Skeleton height={36} width="80%" />
          </div>
        ) : (
          <>
            <div className="field">
              <label>代理</label>
              <label className="radio-row">
                <input
                  type="radio"
                  name="proxy-mode"
                  checked={draft.proxyMode === '' || draft.proxyMode === 'system'}
                  onChange={() => changeProxyMode('system')}
                />
                <span>跟随系统代理（默认）</span>
              </label>
              <label className="radio-row">
                <input type="radio" name="proxy-mode" checked={draft.proxyMode === 'manual'} onChange={() => changeProxyMode('manual')} />
                <span>手动设置</span>
              </label>
              <label className="radio-row">
                <input type="radio" name="proxy-mode" checked={draft.proxyMode === 'direct'} onChange={() => changeProxyMode('direct')} />
                <span>直连（不走代理）</span>
              </label>
              <div className="hint">跟随系统代理：用 macOS 系统设置里的代理（包括 PAC 解析出的第一个代理）；没检测到系统代理时，改读 HTTP_PROXY / HTTPS_PROXY 环境变量。只影响新发起的请求，已在传的文件不受影响。</div>
            </div>
            {systemProxy && (
              <p className="muted" style={{ margin: '0 0 12px' }}>
                {systemProxy.via}
                {systemProxy.pacNote ? ` ${systemProxy.pacNote}` : ''}
              </p>
            )}
            {draft.proxyMode === 'manual' && (
              <>
                <div className="field">
                  <label>HTTP 代理（可选）</label>
                  <input
                    className="input"
                    placeholder="例如：http://127.0.0.1:7890"
                    autoComplete="off"
                    value={draft.proxyHttp}
                    onChange={(e) => setDraft({ ...draft, proxyHttp: e.target.value })}
                  />
                  <div className="hint">格式：http://主机:端口 或 https://主机:端口。留空表示 HTTP(S) 请求不走 HTTP 代理。</div>
                </div>
                <div className="field">
                  <label>SOCKS5 代理（可选）</label>
                  <input
                    className="input"
                    placeholder="例如：socks5://127.0.0.1:1080"
                    autoComplete="off"
                    value={draft.proxySocks5}
                    onChange={(e) => setDraft({ ...draft, proxySocks5: e.target.value })}
                  />
                  <div className="hint">格式：socks5://主机:端口。只有没填 HTTP 代理时才用 SOCKS5（与 macOS 的处理一致）。</div>
                </div>
                <div style={{ display: 'flex', gap: 8, marginBottom: 12, flexWrap: 'wrap' }}>
                  <button className="btn-primary" disabled={busy} onClick={() => void saveDraft(draft)}>
                    保存代理设置
                  </button>
                </div>
                {proxyDirty && <p className="muted">地址改了还没保存，先点“保存代理设置”。</p>}
              </>
            )}
            <div style={{ display: 'flex', gap: 8, marginBottom: 12, flexWrap: 'wrap' }}>
              <button className="btn" disabled={proxyTestBusy || proxyDirty} onClick={() => void runProxyTest()}>
                {proxyTestBusy ? '测试中…' : '测试代理'}
              </button>
            </div>
            {proxyTestResult && <div className="notice-box">{proxyTestResult}</div>}
            <p className="muted">测试代理会对 Dropbox 的 token 端点发一次请求：通了就说明这套代理能连上网盘服务；不通会写清是代理本身没起来、还是代理连不上对方。</p>
          </>
        )}
        </section>
      </Reveal>

      {/* ---- 网盘 ---- */}
      <Reveal>
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
                {reauthTarget?.id === m.id && (
                  <div style={{ fontSize: 13, marginTop: 4 }}>
                    已打开添加向导：按新凭证走完后，这个旧网盘还留在这里——确认新网盘能用后，再点“删除”移除旧的。
                  </div>
                )}
              </div>
              {!m.verified && (
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
                <button className="btn btn-sm" onClick={() => setDeletingMountId(m.id)}>
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
      </Reveal>

      {/* ---- 同步默认 ---- */}
      <Reveal>
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
            <div className="field">
              <label>默认比较方式</label>
              <select
                className="select"
                value={draft.syncCompare}
                onChange={(e) => setDraft({ ...draft, syncCompare: e.target.value as Settings['syncCompare'] })}
              >
                <option value="size+time">大小 + 修改时间</option>
                <option value="size">仅大小</option>
                <option value="checksum">校验值</option>
              </select>
            </div>
            <div className="field">
              <label>冲突处理默认策略</label>
              <select
                className="select"
                value={draft.syncConflict}
                onChange={(e) => setDraft({ ...draft, syncConflict: e.target.value })}
              >
                <option value="keep-both">保留双方：新版占原名，另一份存为冲突副本</option>
                <option value="ask" disabled>
                  每次询问（未实现）
                </option>
                <option value="newer-wins" disabled>
                  新版直接覆盖旧版（未实现）
                </option>
              </select>
              <div className="hint">V0.1 引擎的冲突规则是固定的（新版占原名、另一份保留为冲突副本），其他策略后续版本再提供。</div>
            </div>
            <details className="settings-advanced">
              <summary>高级：并发与限速（引擎暂未读取）</summary>
              <div className="field" style={{ marginTop: 12 }}>
                <label>默认并发数（1–16）</label>
                <input
                  className="input"
                  type="number"
                  min={1}
                  max={16}
                  value={draft.syncConcurrency}
                  onChange={(e) => setDraft({ ...draft, syncConcurrency: Number(e.target.value) })}
                />
                <div className="hint">已保存，但当前同步引擎固定 2 路并发、尚未读取这个设置；引擎支持后才生效，不假装现在就能调。</div>
              </div>
              <div className="field">
                <label>限速（KB/s，0 = 不限速）</label>
                <input
                  className="input"
                  type="number"
                  min={0}
                  value={draft.syncSpeedLimitKBps}
                  onChange={(e) => setDraft({ ...draft, syncSpeedLimitKBps: Number(e.target.value) })}
                />
                <div className="hint">已保存，但当前同步引擎没有限速器；引擎支持后才生效。</div>
              </div>
            </details>
            <div style={{ marginBottom: 0 }}>
              <button className="btn-primary" disabled={busy} onClick={() => void saveDraft(draft)}>
                {t(lang, 'save')}
              </button>
            </div>
          </>
        )}
        </section>
      </Reveal>

      {/* ---- Agent（V0.1 原样保留） ---- */}
      <Reveal>
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
              </div>
              <button className="btn btn-sm" disabled={testingId === a.id} onClick={() => void testAgent(a)}>
                {testingId === a.id ? '测试中…' : '测试连接'}
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
                <button className="btn btn-sm" onClick={() => setDeletingId(a.id)}>
                  {t(lang, 'delete')}
                </button>
              )}
            </div>
          ))
        )}
        </section>
      </Reveal>

      {/* ---- 关于 ---- */}
      <Reveal>
        <section className="settings-card">
        <SectionHead section="about" title={t(lang, 'about')} desc="版本信息。" />
        <p style={{ margin: '4px 0' }}>
          后端版本：<strong>{versions?.backend ?? '—'}</strong>
        </p>
        <p style={{ margin: '4px 0' }}>
          前端版本：<strong>{FRONTEND_VERSION}</strong>
        </p>
        <p style={{ margin: '4px 0' }}>
          协议版本：<strong>{versions?.protocol ?? '—'}</strong>
          {versions?.engine ? ` · 同步引擎 ${versions.engine}` : ''}
        </p>
        <div className="notice-box" style={{ marginBottom: 0 }}>暂未接入检查更新：还没有配置更新源（mygo Updater 需要打包时配置更新 feed），所以这里不假装检查过。当前版本见上方。</div>
        </section>
      </Reveal>

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
            <div className="notice-box">
              API Token 现在不在这里填：桌面端还没有保存 Token 的接口。Token 只在 Agent 启动时配，同步任务真正推到 Agent 上跑的功能在 V0.2 接入。
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
