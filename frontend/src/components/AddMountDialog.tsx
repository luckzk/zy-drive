// AddMountDialog — the "add a drive" wizard.
//   1. pick a provider (prototype Demo v8 .pickgrid: two-column
//      grid of big-logo cards, whole card clickable).
//   2. credentials — two shapes now:
//      - OAuth (Dropbox / Drive / OneDrive): authorize. (A) the
//        backend starts a one-shot loopback listener and the
//        browser jumps back with the code automatically — the
//        frontend polls PollOAuthCallback and exchanges without
//        the user copying anything; manual code paste stays as
//        the fallback. (B) if a builtin ZyDrive official Client
//        ID is configured, a one-click card ("用 ZyDrive 官方应用
//        授权") is the primary path and the self-built app form
//        lives in an "高级" fold; without a builtin ID the
//        self-built form shows directly — never an error, never
//        a blank page.
//      - Key-based (S3 / Drime / local folder): (C) one page —
//        fields + "测试连接" + "保存" on the same page, no step 3.
//        Saving is never blocked by the probe any more: a passed
//        test saves verified; saving without a passed test stores
//        the mount Verified=false ("未验证", marked in the sidebar
//        and Settings, re-testable from Settings → 网盘).
//   3. OAuth only: test & save — the backend probes the root
//      listing first and only persists when the probe succeeds,
//      so a failed test never saves a broken mount.
//
// Dropbox / Drive / OneDrive auth, in order of preference:
//   a. One-click (builtin ZyDrive official app, public client +
//      PKCE, no secret) — when configured.
//   b. Self-built app: paste the app's Client ID / Secret,
//      generate the authorize link, authorize in your own
//      browser, the code jumps back automatically (A); manual
//      paste stays as fallback. OneDrive desktop apps are public
//      clients: PKCE instead of a secret (a confidential web
//      registration still works if a secret is filled in).
//   c. Paste a token directly (fallback): a Dropbox generated
//      access token, a Google refresh token, or a OneDrive
//      refresh token.
// Drime is PAT-only in this wizard: paste a personal access token
// (Settings → Developers → Create token, All permission; never
// expires) plus an optional workspace id. The backend still keeps
// its Drime OAuth code, but the wizard no longer shows it — the
// OAuth fields only confused the form.
// Credentials are typed on the user's own Mac, encrypted at rest
// (AES-256-GCM, master key in the config dir), and never uploaded.
import { useEffect, useRef, useState } from 'react'
import type { AddMountInput, Mount, ProviderKind } from '../mygo'
import { api, friendlyError } from '../api/client'
import { useClosing } from './motion'
import { BrandLogo, IconCheck } from './icons'

type ProviderOption = {
  kind: ProviderKind
  name: string
  desc: string
  // One-line sub under the name (prototype Demo v8 .pick .pp):
  // S3 fills keys, OAuth providers use an authorization code,
  // local picks a folder on this machine.
  sub: string
  available: boolean
  note?: string
}

const PROVIDERS: ProviderOption[] = [
  { kind: 's3', name: 'S3 / R2 / 兼容 S3', desc: 'Amazon S3、Cloudflare R2、MinIO 等，用 AccessKey + SecretKey 接入。', sub: '填 Access Key 添加', available: true },
  { kind: 'dropbox', name: 'Dropbox', desc: '用 OAuth 授权码换取 Token，或直接粘贴 Refresh Token。', sub: 'OAuth 授权码添加', available: true },
  { kind: 'drive', name: 'Google Drive', desc: '用 OAuth 授权码换取 Token，或直接粘贴 Refresh Token。', sub: 'OAuth 授权码添加', available: true },
  { kind: 'local', name: '本地文件夹', desc: '这台 Mac 上的一个文件夹，浏览和同步都能用。', sub: '选本机上的一个文件夹', available: true },
  { kind: 'onedrive', name: 'OneDrive', desc: '用 OAuth 授权码换取 Token（公开客户端，PKCE），或直接粘贴 Refresh Token。个人与工作 / 学校账号都能用。', sub: 'OAuth 授权码添加', available: true },
  { kind: 'drime', name: 'Drime', desc: '用个人访问令牌接入。', sub: '粘贴令牌添加', available: true },
]

const isOAuthProvider = (p: ProviderKind | null): p is 'dropbox' | 'drive' | 'onedrive' =>
  p === 'dropbox' || p === 'drive' || p === 'onedrive'
// Key-based providers (C): one-page form, no step 3.
const isKeyProvider = (p: ProviderKind | null): p is 's3' | 'drime' | 'local' =>
  p === 's3' || p === 'drime' || p === 'local'

const OAUTH_HELP: Record<string, { title: string; steps: string[] }> = {
  dropbox: {
    title: '去哪里拿 Client ID / Secret（Dropbox App Console）',
    steps: [
      '打开 dropbox.com/developers/apps，点 Create app。',
      '选 Scoped access，权限选 Full Dropbox（或 App folder，只访问它自己的文件夹）。',
      'Permissions 页勾选 files.metadata.read、files.metadata.write、files.content.read、files.content.write。',
      'Settings 页拿到 App key（就是 Client ID）和 App secret；Redirect URIs 里加一行 http://localhost（不带端口，运行时用随机端口）。',
      '点“生成授权链接”，在浏览器里登录 Dropbox 点允许；授权完浏览器会自动跳回 ZyDrive，code 自己换成 Token，不用复制。如果浏览器没跳回，再从地址栏复制 code= 后面的值，粘贴回来点“换取 Token”。',
    ],
  },
  drive: {
    title: '去哪里拿 Client ID / Secret（Google Cloud Console）',
    steps: [
      '打开 console.cloud.google.com，新建一个项目。',
      '“API 和服务”里启用 Google Drive API。',
      '“凭据” → 创建凭据 → OAuth 客户端 ID，应用类型选“桌面应用”。',
      '拿到 Client ID 和 Client Secret；OAuth 同意屏幕先把你自己的 Google 账号加为测试用户。',
      'Authorized redirect URIs 里加 http://localhost（不带端口，运行时用随机端口）。点“生成授权链接”，在浏览器里登录 Google 点允许；授权完浏览器会自动跳回 ZyDrive，code 自己换成 Token，不用复制。如果浏览器没跳回，再从地址栏复制 code= 后面的值，粘贴回来点“换取 Token”。',
    ],
  },
  onedrive: {
    title: '去哪里拿 Client ID（Azure 门户 → 应用注册）',
    steps: [
      '打开 portal.azure.com → Microsoft Entra ID → 应用注册 → 新注册，起个名字（例如 ZyDrive）。',
      '“受支持的帐户类型”选“任何组织目录中的帐户和个人 Microsoft 帐户”，个人与工作 / 学校账号就都能用。',
      '注册好后，左侧“身份验证”→ 添加平台 → 选“移动和桌面应用程序”，重定向 URI 填 http://localhost（不带端口，运行时用随机端口），点配置。桌面应用是公开客户端，没有 Secret，也不用建。',
      '左侧“API 权限”→ 添加权限 → Microsoft Graph → 委派的权限 → 勾选 Files.ReadWrite.All 和 offline_access（没有 offline_access 拿不到 Refresh Token），点“代表组织授予管理员同意”（个人账号可跳过）。',
      '左侧“概述”里拿到“应用程序（客户端）ID”，填回来。',
      '点“生成授权链接”，在浏览器里登录微软账号点允许；授权完浏览器会自动跳回 ZyDrive，code 自己换成 Token，不用复制。如果浏览器没跳回，再从地址栏复制 code= 后面的值，粘贴回来点“换取 Token”。',
    ],
  },
}

// OAuthCodeFlow — the one mechanical piece the three OAuth
// providers genuinely share: generate the authorize link, wait
// for the browser to jump back (A: the frontend polls and the
// code exchanges itself), or paste the code by hand when the
// jump-back did not happen. Everything around it (field labels,
// app-console guide, scopes, paste hints) is per-provider in the
// branches below, so the three step-2 forms no longer look — or
// read — like the same form.
function OAuthCodeFlow({
  authUrl,
  code,
  setCode,
  oauthBusy,
  oauthMsg,
  waiting,
  onCancelWait,
  genAuthLink,
  exchangeCode,
  generateLabel,
  generatePrimary,
}: {
  authUrl: string
  code: string
  setCode: (v: string) => void
  oauthBusy: boolean
  oauthMsg: string | null
  waiting: boolean
  onCancelWait: () => void
  genAuthLink: () => void
  exchangeCode: () => void
  // One-click card: the same button, relabelled as the card's
  // primary action ("打开浏览器授权").
  generateLabel?: string
  generatePrimary?: boolean
}) {
  return (
    <>
      <div style={{ display: 'flex', gap: 8, marginBottom: 12 }}>
        <button className={generatePrimary ? 'btn-primary' : 'btn'} disabled={oauthBusy} onClick={genAuthLink}>
          {oauthBusy ? '生成中…' : (generateLabel ?? '生成授权链接')}
        </button>
      </div>
      {authUrl && (
        <>
          <div className="field">
            <label>授权链接（复制到浏览器里打开，或点下面的按钮）</label>
            <textarea className="textarea" readOnly rows={3} value={authUrl} onFocus={(e) => e.target.select()} />
          </div>
          <div style={{ display: 'flex', gap: 8, marginBottom: 12 }}>
            <button className="btn" onClick={() => window.open(authUrl, '_blank', 'noopener')}>
              在浏览器里打开
            </button>
            {waiting && (
              <button className="btn" onClick={onCancelWait}>
                取消等待
              </button>
            )}
          </div>
          {waiting && (
            <div className="notice-box">
              已打开授权页：等你在浏览器里点允许，拿到 code 会自动换 Token，不用复制回来。浏览器没跳回的话，把地址栏里的 code 粘贴到下面，点“换取 Token”。
            </div>
          )}
          <div className="field">
            <label>授权码（code，浏览器没自动跳回时才需要手动粘贴）</label>
            <input className="input" placeholder="粘贴 code= 后面的值" autoComplete="off" value={code} onChange={(e) => setCode(e.target.value)} />
          </div>
          <div style={{ display: 'flex', gap: 8, marginBottom: 12 }}>
            <button className="btn" disabled={oauthBusy} onClick={exchangeCode}>
              {oauthBusy ? '换取中…' : '换取 Token'}
            </button>
          </div>
        </>
      )}
      {oauthMsg && <div className="notice-box">{oauthMsg}</div>}
    </>
  )
}

export default function AddMountDialog({
  onClose,
  onAdded,
  initialProvider,
  initialStep,
  initialName,
  initialGroup,
}: {
  onClose: () => void
  onAdded: (m: Mount) => void
  // Re-auth entry (from Settings → 网盘 → 重新授权): open straight at
  // the credentials step for that provider, with the old name/group
  // prefilled. It still creates a new mount — the old one is removed
  // separately from Settings, never silently.
  initialProvider?: ProviderKind
  initialStep?: 1 | 2
  initialName?: string
  initialGroup?: string
}) {
  const [closing, requestClose] = useClosing(onClose)
  const [step, setStep] = useState<1 | 2 | 3>(initialStep ?? 1)
  const [provider, setProvider] = useState<ProviderKind | null>(initialProvider ?? null)
  const [displayName, setDisplayName] = useState(initialName ?? '')
  const [group, setGroup] = useState(initialGroup ?? '')
  // S3
  const [endpoint, setEndpoint] = useState('')
  const [region, setRegion] = useState('us-east-1')
  const [bucket, setBucket] = useState('')
  const [prefix, setPrefix] = useState('')
  const [pathStyle, setPathStyle] = useState(false)
  const [accessKey, setAccessKey] = useState('')
  const [secretKey, setSecretKey] = useState('')
  // OAuth
  const [clientId, setClientId] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [accessToken, setAccessToken] = useState('')
  const [refreshToken, setRefreshToken] = useState('')
  const [authUrl, setAuthUrl] = useState('')
  const [oauthState, setOauthState] = useState('')
  const [code, setCode] = useState('')
  const [oauthBusy, setOauthBusy] = useState(false)
  const [oauthMsg, setOauthMsg] = useState<string | null>(null)
  // (B) builtin one-click client id: null = not loaded yet,
  // '' = official app not registered (self-built flow only).
  const [builtinClientId, setBuiltinClientId] = useState<string | null>(null)
  // The Client ID / Secret the current authorize link was built
  // with: the exchange must use these, not whatever the form
  // holds now (the one-click flow never fills the form fields).
  const [linkClientId, setLinkClientId] = useState('')
  const [linkClientSecret, setLinkClientSecret] = useState('')
  // (A) auto-wait: the frontend polls PollOAuthCallback while the
  // user authorizes in the browser.
  const [waiting, setWaiting] = useState(false)
  const pollTimer = useRef<number | null>(null)
  const pollCount = useRef(0)
  const oauthStateRef = useRef('')
  // Local
  const [localPath, setLocalPath] = useState('')
  // Drime
  const [workspaceId, setWorkspaceId] = useState('')
  // (C) one-page form test state. testedKey is the exact input the
  // last passed test ran against: editing any field after a pass
  // makes the test stale, and saving then stores unverified.
  const [testState, setTestState] = useState<'idle' | 'testing' | 'passed' | 'failed'>('idle')
  const [testMsg, setTestMsg] = useState<string | null>(null)
  const [testedKey, setTestedKey] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  // Step 3 (OAuth only)
  const [testing, setTesting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [formError, setFormError] = useState<string | null>(null)

  const chosen = PROVIDERS.find((p) => p.kind === provider) ?? null
  const totalSteps = isKeyProvider(provider) ? 2 : 3
  const stepLabel = step === 1 ? '选厂商' : step === 2 ? (isKeyProvider(provider) ? '填表单（一页）' : '授权') : '测试并保存'

  const stopWaiting = (cancelRemote: boolean) => {
    if (pollTimer.current !== null) {
      window.clearInterval(pollTimer.current)
      pollTimer.current = null
    }
    setWaiting(false)
    if (cancelRemote && oauthStateRef.current) {
      const st = oauthStateRef.current
      oauthStateRef.current = ''
      void api.cancelOAuth(st).catch(() => undefined)
    }
  }

  // Dialog closed (or unmounted) mid-authorization: stop polling
  // and free the loopback port instead of waiting out the expiry.
  useEffect(
    () => () => {
      if (pollTimer.current !== null) window.clearInterval(pollTimer.current)
      if (oauthStateRef.current) void api.cancelOAuth(oauthStateRef.current).catch(() => undefined)
    },
    []
  )

  // Entering the OAuth step: ask the backend whether a builtin
  // ZyDrive official Client ID is configured, and reset any
  // leftover authorize state from a previous provider.
  useEffect(() => {
    if (step !== 2 || !isOAuthProvider(provider)) return
    stopWaiting(false)
    setAuthUrl('')
    setCode('')
    setOauthMsg(null)
    setBuiltinClientId(null)
    void api
      .builtinOAuthClientID(provider)
      .then((id) => setBuiltinClientId(id ?? ''))
      .catch(() => setBuiltinClientId(''))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [step, provider])

  const buildInput = (): AddMountInput => ({
    displayName: displayName.trim(),
    provider: provider as ProviderKind,
    group: group.trim() || undefined,
    endpoint: endpoint.trim() || undefined,
    region: region.trim() || undefined,
    bucket: bucket.trim() || undefined,
    prefix: prefix.trim() || undefined,
    pathStyle: pathStyle || undefined,
    accessKey: accessKey || undefined,
    secretKey: secretKey || undefined,
    accessToken: accessToken || undefined,
    refreshToken: refreshToken || undefined,
    clientId: clientId.trim() || undefined,
    clientSecret: clientSecret || undefined,
    workspaceId: workspaceId.trim() || undefined,
    localPath: localPath.trim() || undefined,
  })

  const validateStep2 = (): string | null => {
    if (!displayName.trim()) return '请给网盘起个名字。'
    switch (provider) {
      case 's3':
        if (!bucket.trim()) return '请填桶名（Bucket）。'
        if (bucket.trim().includes('/')) return '桶名里不能有 /：桶名就是一个名字，桶里面的子文件夹请填到“桶内前缀”里。'
        {
          const p = prefix.trim().replace(/^\/+|\/+$/g, '')
          const b = bucket.trim()
          if (p === b) return '桶内前缀不能就是桶名本身：前缀是桶里面的子文件夹，例如 photos/2024；想挂整个桶就把前缀留空。'
          if (p.startsWith(`${b}/`)) return '桶内前缀不要把桶名再填一遍：前缀是桶里面的子文件夹，例如 photos/2024；想挂整个桶就把前缀留空。'
        }
        if (!accessKey.trim() || !secretKey.trim()) return '请填 AccessKey 和 SecretKey。'
        return null
      case 'dropbox':
      case 'drive':
      case 'onedrive':
        if (!accessToken.trim() && !refreshToken.trim()) return '请先用授权码换取 Token，或直接粘贴 Access Token / Refresh Token。'
        return null
      case 'drime':
        // PAT-only: the wizard no longer shows any OAuth fields for
        // Drime, so only the personal access token is required.
        if (!accessToken.trim()) return '请粘贴个人访问令牌（Access Token）。'
        if (workspaceId.trim() !== '' && !/^\d+$/.test(workspaceId.trim())) return '工作区 ID 只能是数字，例如 8762；留空就是个人空间。'
        return null
      case 'local':
        if (!localPath.trim()) return '请填本地文件夹的绝对路径，例如 /Users/zy/照片。'
        if (!localPath.trim().startsWith('/')) return '本地路径必须是绝对路径，以 / 开头。'
        return null
      default:
        return '请先选一个网盘。'
    }
  }

  const goStep3 = () => {
    const err = validateStep2()
    if (err) {
      setFormError(err)
      return
    }
    setFormError(null)
    setError(null)
    setStep(3)
  }

  const testAndSave = async () => {
    setTesting(true)
    setError(null)
    try {
      const m = await api.addMount(buildInput())
      if (!m) throw new Error('添加失败：后端没有返回网盘信息。')
      onAdded(m)
    } catch (e) {
      setError(friendlyError(e))
    } finally {
      setTesting(false)
    }
  }

  // (C) one-page form: test the config without saving anything.
  // A pass records the exact input it ran against; editing any
  // field afterwards makes it stale.
  const testConnection = async () => {
    const err = validateStep2()
    if (err) {
      setFormError(err)
      return
    }
    setFormError(null)
    setTestState('testing')
    setTestMsg(null)
    try {
      await api.testMountConfig(buildInput())
      setTestState('passed')
      setTestedKey(JSON.stringify(buildInput()))
      setTestMsg('连接测试通过，可以保存了。')
    } catch (e) {
      setTestState('failed')
      setTestedKey(null)
      setTestMsg(friendlyError(e))
    }
  }

  // (C) one-page form: save. A fresh passed test saves verified;
  // anything else saves unverified ("未验证"，侧栏会标出来，之后
  // 在设置页 → 网盘 → 重新测试）— saving is the user's choice,
  // the test is only information.
  const saveKeyMount = async () => {
    const err = validateStep2()
    if (err) {
      setFormError(err)
      return
    }
    setFormError(null)
    setSaving(true)
    try {
      const fresh = testedKey !== null && testedKey === JSON.stringify(buildInput())
      const m = await api.addMount({ ...buildInput(), skipProbe: !fresh })
      if (!m) throw new Error('添加失败：后端没有返回网盘信息。')
      onAdded(m)
    } catch (e) {
      setFormError(friendlyError(e))
    } finally {
      setSaving(false)
    }
  }

  const startWaiting = (state: string) => {
    stopWaiting(false)
    setWaiting(true)
    pollCount.current = 0
    pollTimer.current = window.setInterval(() => {
      pollCount.current += 1
      if (pollCount.current > 200) {
        // ~5 minutes: stop asking and say so in plain language.
        // The backend keeps the state until its own 10-minute
        // expiry, so a late jump-back can still be pasted by hand.
        stopWaiting(true)
        setOauthMsg('等了 5 分钟，浏览器还没有跳回（可能本机回调端口被挡住了）：请复制地址栏里的 code 粘贴到下面，点“换取 Token”。')
        return
      }
      void api
        .pollOAuthCallback(state)
        .then((res) => {
          if (!res) return
          if (res.status === 'done' && res.code) {
            stopWaiting(false)
            setCode(res.code)
            void exchangeCode(res.code)
          } else if (res.status === 'error' || res.status === 'expired') {
            stopWaiting(false)
            setOauthMsg(res.message || '浏览器没有跳回，请复制地址栏里的 code 粘贴到下面，点“换取 Token”。')
          }
          // "waiting": keep polling.
        })
        .catch(() => {
          // Transient Bind failure: keep polling, the next tick
          // retries. The 5-minute cap above is the only exit.
        })
    }, 1500)
  }

  const genAuthLink = async (id: string, secret: string) => {
    if (!provider || !isOAuthProvider(provider)) return
    if (!id.trim()) {
      setOauthMsg('请先填 Client ID。')
      return
    }
    // A previous link (other provider, or a retry) is dropped
    // before minting a new one, so its port is freed at once.
    if (oauthStateRef.current) {
      void api.cancelOAuth(oauthStateRef.current).catch(() => undefined)
      oauthStateRef.current = ''
    }
    setOauthBusy(true)
    setOauthMsg(null)
    try {
      const start = await api.beginOAuth(provider, id.trim())
      if (!start) throw new Error('生成授权链接失败。')
      setAuthUrl(start.authUrl)
      setOauthState(start.state)
      oauthStateRef.current = start.state
      setLinkClientId(id.trim())
      setLinkClientSecret(secret)
      setOauthMsg('链接已生成：在浏览器里打开它，授权后会自动跳回 ZyDrive，code 会自己换成 Token；如果浏览器没跳回，再把地址栏里的 code 粘贴回来。')
      startWaiting(start.state)
    } catch (e) {
      setOauthMsg(friendlyError(e))
    } finally {
      setOauthBusy(false)
    }
  }

  const exchangeCode = async (autoCode?: string) => {
    if (!provider || !isOAuthProvider(provider)) return
    const useCode = (autoCode ?? code).trim()
    if (!useCode) {
      setOauthMsg('请先粘贴授权码（code）。')
      return
    }
    setOauthBusy(true)
    setOauthMsg(null)
    try {
      // The exchange uses the Client ID / Secret the link was
      // built with — the one-click flow never fills the form
      // fields, so reading them here would exchange with "".
      const useClientId = linkClientId || clientId.trim()
      const useSecret = linkClientId ? linkClientSecret : clientSecret
      const tokens = await api.exchangeOAuthCode(provider, useClientId, useSecret, useCode, oauthState, '')
      if (!tokens) throw new Error('换取 Token 失败。')
      setAccessToken(tokens.accessToken)
      if (tokens.refreshToken) setRefreshToken(tokens.refreshToken)
      setOauthMsg(tokens.refreshToken ? 'Token 已换取成功（拿到 Refresh Token，长期有效）。' : 'Token 已换取成功（只拿到 Access Token，过期后需重新授权）。')
    } catch (e) {
      setOauthMsg(friendlyError(e))
    } finally {
      setOauthBusy(false)
    }
  }

  // The self-built app form (B, "高级"): per-provider fields,
  // guide, scopes and paste hints — the same JSX whether it is
  // shown directly (no builtin client configured) or inside the
  // "高级：用自己的应用" fold (builtin one-click is primary).
  const selfBuiltForm = (
    <>
      {/* Dropbox — its own form: App key / App secret from the
          Dropbox App Console, Dropbox scopes, Dropbox paste hint.
          It shares only the link/code mechanics (OAuthCodeFlow)
          with Drive/OneDrive, never the fields or copy. */}
      {provider === 'dropbox' && (
        <>
          <details style={{ marginBottom: 12 }}>
            <summary style={{ cursor: 'pointer' }}>{OAUTH_HELP.dropbox.title}</summary>
            <ol style={{ margin: '8px 0', paddingLeft: 20 }}>
              {OAUTH_HELP.dropbox.steps.map((s, i) => (
                <li key={i} className="muted" style={{ marginBottom: 4 }}>
                  {s}
                </li>
              ))}
            </ol>
          </details>
          <div className="field">
            <label>App key（就是 Client ID）</label>
            <input className="input" autoComplete="off" value={clientId} onChange={(e) => setClientId(e.target.value)} />
          </div>
          <div className="field">
            <label>App secret（Client Secret）</label>
            <input className="input" type="password" autoComplete="off" value={clientSecret} onChange={(e) => setClientSecret(e.target.value)} />
          </div>
          <p className="muted">本次授权请求的权限：files.metadata.read、files.content.read、files.content.write、files.metadata.write（在应用的 Permissions 页勾选）。</p>
          <OAuthCodeFlow
            authUrl={authUrl}
            code={code}
            setCode={setCode}
            oauthBusy={oauthBusy}
            oauthMsg={oauthMsg}
            waiting={waiting}
            onCancelWait={() => stopWaiting(true)}
            genAuthLink={() => void genAuthLink(clientId, clientSecret)}
            exchangeCode={() => void exchangeCode()}
          />
          <div className="field">
            <label>Access Token（已通过授权码换取会自动填上；也可直接粘贴）</label>
            <input className="input" type="password" autoComplete="off" value={accessToken} onChange={(e) => setAccessToken(e.target.value)} />
          </div>
          <div className="field">
            <label>Refresh Token（长期有效；有它过期后会自动刷新）</label>
            <input className="input" type="password" autoComplete="off" value={refreshToken} onChange={(e) => setRefreshToken(e.target.value)} />
          </div>
          <p className="muted">
            直接粘贴也可以：Dropbox App Console → 应用 → Settings → 生成的访问令牌（短期，几小时过期）；要长期有效请走上面的授权码流程拿 Refresh Token。
          </p>
        </>
      )}

      {/* Google Drive — its own form: OAuth 客户端 ID / 客户端密钥
          from Google Cloud Console, Drive scope, Playground hint. */}
      {provider === 'drive' && (
        <>
          <details style={{ marginBottom: 12 }}>
            <summary style={{ cursor: 'pointer' }}>{OAUTH_HELP.drive.title}</summary>
            <ol style={{ margin: '8px 0', paddingLeft: 20 }}>
              {OAUTH_HELP.drive.steps.map((s, i) => (
                <li key={i} className="muted" style={{ marginBottom: 4 }}>
                  {s}
                </li>
              ))}
            </ol>
          </details>
          <div className="field">
            <label>OAuth 客户端 ID</label>
            <input className="input" autoComplete="off" value={clientId} onChange={(e) => setClientId(e.target.value)} />
          </div>
          <div className="field">
            <label>客户端密钥（Client Secret）</label>
            <input className="input" type="password" autoComplete="off" value={clientSecret} onChange={(e) => setClientSecret(e.target.value)} />
          </div>
          <p className="muted">本次授权请求的权限：https://www.googleapis.com/auth/drive（完整访问 Drive；需先在“API 和服务”里启用 Google Drive API）。</p>
          <OAuthCodeFlow
            authUrl={authUrl}
            code={code}
            setCode={setCode}
            oauthBusy={oauthBusy}
            oauthMsg={oauthMsg}
            waiting={waiting}
            onCancelWait={() => stopWaiting(true)}
            genAuthLink={() => void genAuthLink(clientId, clientSecret)}
            exchangeCode={() => void exchangeCode()}
          />
          <div className="field">
            <label>Access Token（已通过授权码换取会自动填上；也可直接粘贴）</label>
            <input className="input" type="password" autoComplete="off" value={accessToken} onChange={(e) => setAccessToken(e.target.value)} />
          </div>
          <div className="field">
            <label>Refresh Token（长期有效；有它过期后会自动刷新）</label>
            <input className="input" type="password" autoComplete="off" value={refreshToken} onChange={(e) => setRefreshToken(e.target.value)} />
          </div>
          <p className="muted">
            直接粘贴也可以：Refresh Token 可在 Google OAuth Playground（需选 Drive 权限、勾选 offline）里拿到；只有 Access Token 的话过期后需重新授权。
          </p>
        </>
      )}

      {/* OneDrive — its own form: 应用程序（客户端）ID from the
          Azure portal; desktop apps are public clients (PKCE),
          so the secret is optional. Graph scopes, not Drive's. */}
      {provider === 'onedrive' && (
        <>
          <details style={{ marginBottom: 12 }}>
            <summary style={{ cursor: 'pointer' }}>{OAUTH_HELP.onedrive.title}</summary>
            <ol style={{ margin: '8px 0', paddingLeft: 20 }}>
              {OAUTH_HELP.onedrive.steps.map((s, i) => (
                <li key={i} className="muted" style={{ marginBottom: 4 }}>
                  {s}
                </li>
              ))}
            </ol>
          </details>
          <div className="field">
            <label>应用程序（客户端）ID</label>
            <input className="input" autoComplete="off" value={clientId} onChange={(e) => setClientId(e.target.value)} />
          </div>
          <div className="field">
            <label>Client Secret（可选：桌面应用是公开客户端，不用填；网页应用才需要）</label>
            <input className="input" type="password" autoComplete="off" value={clientSecret} onChange={(e) => setClientSecret(e.target.value)} />
          </div>
          <p className="muted">本次授权请求的权限：Files.ReadWrite.All、offline_access（没有 offline_access 拿不到 Refresh Token）。</p>
          <OAuthCodeFlow
            authUrl={authUrl}
            code={code}
            setCode={setCode}
            oauthBusy={oauthBusy}
            oauthMsg={oauthMsg}
            waiting={waiting}
            onCancelWait={() => stopWaiting(true)}
            genAuthLink={() => void genAuthLink(clientId, clientSecret)}
            exchangeCode={() => void exchangeCode()}
          />
          <div className="field">
            <label>Access Token（已通过授权码换取会自动填上；也可直接粘贴）</label>
            <input className="input" type="password" autoComplete="off" value={accessToken} onChange={(e) => setAccessToken(e.target.value)} />
          </div>
          <div className="field">
            <label>Refresh Token（长期有效；有它过期后会自动刷新）</label>
            <input className="input" type="password" autoComplete="off" value={refreshToken} onChange={(e) => setRefreshToken(e.target.value)} />
          </div>
          <p className="muted">
            直接粘贴也可以：Refresh Token 可在 Microsoft 的 OAuth 授权流程里拿到（权限须含 offline_access）；只有 Access Token 的话过期后需重新授权。
          </p>
        </>
      )}
    </>
  )

  return (
    <div className={`dialog-backdrop${closing ? ' closing' : ''}`} onClick={requestClose}>
      <div className={`dialog dialog-wide${closing ? ' closing' : ''}`} onClick={(e) => e.stopPropagation()}>
        <h1>添加网盘</h1>
        <p className="muted" style={{ margin: '0 0 12px' }}>
          第 {step} 步 / 共 {totalSteps} 步：{stepLabel}
        </p>

        {step === 1 && (
          <>
            {/* Prototype Demo v8 .pickgrid: two-column grid of big-logo
                cards (32px brand logo + name + one-line sub), the
                whole card clickable; selected = brand-green border +
                pale green fill (.pick.on). Not the old small list
                rows with a "选择" button. No "推荐" badge and no
                grouping in the prototype, so none here either. */}
            <div className="provider-grid">
              {PROVIDERS.map((p) => (
                <button
                  key={p.kind}
                  type="button"
                  className={`provider-card${provider === p.kind ? ' selected' : ''}`}
                  aria-pressed={provider === p.kind}
                  onClick={() => p.available && setProvider(p.kind)}
                >
                  <BrandLogo provider={p.kind} size={32} />
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <span className="provider-name">{p.name}</span>
                    <span className="provider-sub" style={{ display: 'block' }}>
                      {p.sub}
                    </span>
                    <span className="provider-sub" style={{ display: 'block' }}>
                      {p.desc}
                    </span>
                  </span>
                  {provider === p.kind && (
                    <span className="provider-check" aria-label="已选">
                      <IconCheck />
                    </span>
                  )}
                </button>
              ))}
            </div>
            <div className="dialog-actions">
              <button className="btn" onClick={requestClose}>
                取消
              </button>
              <button className="btn-primary" disabled={!provider} onClick={() => setStep(2)}>
                下一步
              </button>
            </div>
          </>
        )}

        {step === 2 && chosen && isKeyProvider(provider) && (
          <>
            {/* (C) one-page form: fields + test + save on the same
                page. No step 3 for S3 / Drime / local. */}
            <div className="field">
              <label>名字</label>
              <input className="input" placeholder="例如：工作 Drive、R2 备份" value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
            </div>
            <div className="field">
              <label>分组（可选）</label>
              <input className="input" placeholder="例如：工作、个人，留空为“未分组”" value={group} onChange={(e) => setGroup(e.target.value)} />
            </div>

            {provider === 's3' && (
              <>
                <div className="field">
                  <label>Endpoint（S3 兼容地址；用 Amazon S3 请留空）</label>
                  <input
                    className="input"
                    placeholder="R2 例：https://&lt;账号 ID&gt;.r2.cloudflarestorage.com；MinIO 例：http://192.168.1.10:9000"
                    value={endpoint}
                    onChange={(e) => setEndpoint(e.target.value)}
                  />
                </div>
                <div className="field">
                  <label>区域（Region）</label>
                  <input className="input" value={region} onChange={(e) => setRegion(e.target.value)} />
                </div>
                <div className="field">
                  <label>桶名（Bucket）</label>
                  <input className="input" placeholder="例如：my-backup" value={bucket} onChange={(e) => setBucket(e.target.value)} />
                </div>
                <div className="field">
                  <label>桶内前缀（可选）</label>
                  <input className="input" placeholder="例如：photos/2024" value={prefix} onChange={(e) => setPrefix(e.target.value)} />
                  <div className="hint">留空 = 整个桶。只填桶里面的子文件夹，例如 photos/2024，不要把桶名再填一遍。</div>
                </div>
                <label style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 12 }}>
                  <input type="checkbox" checked={pathStyle} onChange={(e) => setPathStyle(e.target.checked)} />
                  路径风格地址（MinIO / 自建网关一般要勾上，R2 不用）
                </label>
                <div className="field">
                  <label>AccessKey</label>
                  <input className="input" autoComplete="off" value={accessKey} onChange={(e) => setAccessKey(e.target.value)} />
                </div>
                <div className="field">
                  <label>SecretKey</label>
                  <input className="input" type="password" autoComplete="off" value={secretKey} onChange={(e) => setSecretKey(e.target.value)} />
                </div>
                <p className="muted">AccessKey / SecretKey 去哪里拿：AWS 在 IAM → 用户 → 安全凭证；R2 在 Cloudflare 控制台 → R2 → 管理 R2 API 令牌；MinIO 在控制台 → Access Keys。</p>
              </>
            )}

            {provider === 'drime' && (
              <>
                <div className="notice-box" style={{ marginBottom: 12 }}>
                  个人访问令牌去哪里拿：Drime 设置 → Developers → Create token，权限选 All，令牌只显示一次、永不过期，复制下来粘贴到下面那栏。不用建应用、不用在浏览器里授权，直接粘贴就能用。
                </div>
                <div className="field">
                  <label>个人访问令牌（Access Token）</label>
                  <input className="input" type="password" autoComplete="off" value={accessToken} onChange={(e) => setAccessToken(e.target.value)} />
                </div>
                <div className="field">
                  <label>工作区 ID（可选，留空为个人空间）</label>
                  <input className="input" placeholder="例如：8762" autoComplete="off" value={workspaceId} onChange={(e) => setWorkspaceId(e.target.value)} />
                  <div className="hint">只挂个人空间就留空；要挂某个团队空间，填它的数字 ID。</div>
                </div>
              </>
            )}

            {provider === 'local' && (
              <>
                <div className="field">
                  <label>本地文件夹绝对路径</label>
                  <input className="input" placeholder="例如：/Users/zy/照片" value={localPath} onChange={(e) => setLocalPath(e.target.value)} />
                </div>
                <p className="muted">填这台 Mac 上的完整路径。同步任务里选本地文件夹时，同样可以直接用这个网盘。</p>
              </>
            )}

            {formError && <div className="error-box">{formError}</div>}
            {testMsg && <div className={testState === 'passed' ? 'notice-box' : 'error-box'}>{testMsg}</div>}
            <p className="muted">
              {testState === 'passed' && testedKey === JSON.stringify(buildInput())
                ? '已通过测试，点保存即存为已验证。'
                : '测试通过 → 点保存即存为已验证；没测或测试失败也能保存，会标成“未验证”，之后在设置页 → 网盘 → 重新测试。'}
            </p>
            <div className="notice-box">凭证只保存在你这台 Mac 上（加密存储），不会上传到任何地方。</div>
            <div className="dialog-actions">
              <button className="btn" disabled={saving || testState === 'testing'} onClick={() => setStep(1)}>
                上一步
              </button>
              <button className="btn" disabled={saving || testState === 'testing'} onClick={requestClose}>
                取消
              </button>
              <button className="btn" disabled={saving || testState === 'testing'} onClick={() => void testConnection()}>
                {testState === 'testing' ? '测试中…' : '测试连接'}
              </button>
              <button className="btn-primary" disabled={saving || testState === 'testing'} onClick={() => void saveKeyMount()}>
                {saving ? '保存中…' : '保存'}
              </button>
            </div>
          </>
        )}

        {step === 2 && chosen && isOAuthProvider(provider) && (
          <>
            <div className="field">
              <label>名字</label>
              <input className="input" placeholder="例如：工作 Drive、R2 备份" value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
            </div>
            <div className="field">
              <label>分组（可选）</label>
              <input className="input" placeholder="例如：工作、个人，留空为“未分组”" value={group} onChange={(e) => setGroup(e.target.value)} />
            </div>

            {/* (B) one-click with the builtin ZyDrive official app.
                Shown only when a builtin Client ID is configured;
                otherwise the notice below explains why, and the
                self-built form is the whole step. */}
            {builtinClientId !== null && builtinClientId !== '' && (
              <>
                <div className="notice-box" style={{ borderColor: 'var(--brand)' }}>
                  <div style={{ marginBottom: 4 }}>一键授权（ZyDrive 官方应用）</div>
                  <p className="muted" style={{ margin: '0 0 8px' }}>
                    不用自己建应用：点授权，在浏览器里登录点允许，code 会自动跳回、自动换 Token。
                  </p>
                  <OAuthCodeFlow
                    authUrl={authUrl}
                    code={code}
                    setCode={setCode}
                    oauthBusy={oauthBusy}
                    oauthMsg={oauthMsg}
                    waiting={waiting}
                    onCancelWait={() => stopWaiting(true)}
                    genAuthLink={() => void genAuthLink(builtinClientId, '')}
                    exchangeCode={() => void exchangeCode()}
                    generateLabel="打开浏览器授权"
                    generatePrimary
                  />
                  <p className="muted" style={{ margin: '8px 0 0' }}>
                    用的是 ZyDrive 官方共享应用（公开客户端，PKCE，不带 Secret）。共享应用有被限流或停用的风险（rclone
                    的共享客户端正在被逐步停用），想完全自己掌控就用下面的“高级：用自己的应用”。
                  </p>
                </div>
              </>
            )}
            {builtinClientId === '' && (
              <div className="notice-box">
                一键授权还没配置：这个网盘的 ZyDrive 官方应用还没注册好，下面先用自己的应用走授权；注册好后这里会出现一键授权按钮。
              </div>
            )}

            {builtinClientId !== null && builtinClientId !== '' ? (
              <details className="settings-advanced">
                <summary>高级：用自己的应用（Client ID / Secret）</summary>
                <div style={{ marginTop: 12 }}>{selfBuiltForm}</div>
              </details>
            ) : (
              selfBuiltForm
            )}

            {formError && <div className="error-box">{formError}</div>}
            <div className="notice-box">凭证只保存在你这台 Mac 上（加密存储），不会上传到任何地方。</div>
            <div className="dialog-actions">
              <button className="btn" onClick={() => setStep(1)}>
                上一步
              </button>
              <button className="btn" onClick={requestClose}>
                取消
              </button>
              <button className="btn-primary" onClick={goStep3}>
                下一步：测试
              </button>
            </div>
          </>
        )}

        {step === 3 && chosen && isOAuthProvider(provider) && (
          <>
            <p>
              准备添加 <strong>{displayName.trim() || chosen.name}</strong>（{chosen.name}
              {group.trim() ? ` · 分组“${group.trim()}”` : ''}）。
            </p>
            {provider === 'onedrive' && (
              <p>
                将挂载：OneDrive（Microsoft 账号，个人 / 工作 / 学校账号均可）。
              </p>
            )}
            <p className="muted">点“测试并保存”后会先连一次网盘根目录，通了才保存；不通不会保存，也不会留下坏网盘。</p>
            {testing && (
              <div className="notice-box">
                正在连接测试网盘，请稍候…
              </div>
            )}
            {error && (
              <>
                <div className="error-box">{error}</div>
                <div style={{ display: 'flex', gap: 8, marginBottom: 12 }}>
                  <button className="btn-primary" disabled={testing} onClick={() => void testAndSave()}>
                    重试
                  </button>
                  <button className="btn" disabled={testing} onClick={() => setStep(2)}>
                    返回修改凭证
                  </button>
                </div>
              </>
            )}
            <div className="dialog-actions">
              <button className="btn" disabled={testing} onClick={() => setStep(2)}>
                上一步
              </button>
              <button className="btn" disabled={testing} onClick={requestClose}>
                取消
              </button>
              {!error && (
                <button className="btn-primary" disabled={testing} onClick={() => void testAndSave()}>
                  {testing ? '测试中…' : '测试并保存'}
                </button>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  )
}
