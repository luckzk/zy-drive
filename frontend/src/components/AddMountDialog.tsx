// SPDX-License-Identifier: MIT
// AddMountDialog — the "add a drive" wizard.
//   1. pick a provider (prototype Demo v8 .pickgrid: two-column
//      grid of big-logo cards, whole card clickable).
//   2. credentials — two shapes now:
//      - OAuth (Dropbox / Drive / OneDrive): authorize. (A) the
//        backend starts a one-shot loopback listener and the
//        browser jumps back with the code automatically — the
//        frontend polls PollOAuthCallback and exchanges without
//        the user copying anything; manual code paste stays as
//        the fallback. (B) the authorization source is an
//        explicit two-choice card row — "官方应用（一键授权）"
//        vs "自己的应用" — never a <details> triangle fold:
//        the builtin ZyDrive official Client ID is the primary
//        path, the user's own app (stored separately in
//        custom-oauth-clients.json, never overwriting the
//        builtin) is the second card; switching back to official
//        always finds the builtin intact. Without a builtin ID
//        the official card is disabled and "自己的应用" is the
//        default — never an error, never a blank page. The
//        per-provider app-console guide is an always-visible
//        info box, also never a <details> fold.
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
// Dialog layout: .dialog-flex — the title and the footer stay
// fixed; only the .dialog-body scrolls. Step 1's provider grid
// is never covered by the footer, and long step-2 forms scroll
// to the bottom with the buttons always visible.
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
import type { AddMountInput, CustomOAuthApp, Mount, ProviderKind } from '../mygo'
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
  { kind: 'box', name: 'Box', desc: '用 OAuth 授权码换取 Token，或直接粘贴 Refresh Token。需要自己在 Box 开发者控制台建应用（换 Token 要 Client Secret，没有一键授权）。', sub: 'OAuth 授权码添加', available: true },
  { kind: 'pcloud', name: 'pCloud', desc: '用 OAuth 授权码换取 Token，或直接粘贴 Access Token。需要自己在 pCloud 控制台建应用（换 Token 要 Client Secret，没有一键授权）；账号分美区 / 欧区。', sub: 'OAuth 授权码添加', available: true },
  { kind: 'mega', name: 'MEGA', desc: '用邮箱 + 密码登录（可带两步验证）。文件在 MEGA 端自带加密。', sub: '邮箱 + 密码添加', available: true },
]

const isOAuthProvider = (p: ProviderKind | null): p is 'dropbox' | 'drive' | 'onedrive' | 'box' | 'pcloud' =>
  p === 'dropbox' || p === 'drive' || p === 'onedrive' || p === 'box' || p === 'pcloud'
// Key-based providers (C): one-page form, no step 3.
const isKeyProvider = (p: ProviderKind | null): p is 's3' | 'drime' | 'local' | 'mega' =>
  p === 's3' || p === 'drime' || p === 'local' || p === 'mega'

const OAUTH_HELP: Record<string, { title: string; steps: string[] }> = {
  dropbox: {
    title: '去哪里拿 Client ID / Secret（Dropbox App Console）',
    steps: [
      '打开 dropbox.com/developers/apps，点 Create app。',
      '选 Scoped access，权限选 Full Dropbox（或 App folder，只访问它自己的文件夹）。',
      'Permissions 页勾选 files.metadata.read、files.metadata.write、files.content.read、files.content.write、account_info.read（最后一项是容量显示要的，没有它文件照常用、容量会报 missing_scope）。',
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
  box: {
    title: '去哪里拿 Client ID / Secret（Box 开发者控制台）',
    steps: [
      '打开 app.box.com/developers/console，点 Create New App，选 Custom App，名字例如 ZyDrive。',
      '应用类型选 User Authentication (OAuth 2.0)，不要选 Server Authentication（那是 JWT / 服务账号，走另一套）。',
      '左侧 Configuration → OAuth 2.0 Redirect URI 里加一行 http://localhost:57103（带端口，固定端口 57103）。',
      '同一页 Scopes 里勾选 Read and write all files and folders stored in Box（root_readwrite）。',
      'Configuration 页拿到 Client ID 和 Client Secret，填回来。',
      '点“生成授权链接”，在浏览器里登录 Box 点允许；授权完浏览器会自动跳回 ZyDrive，code 自己换成 Token，不用复制。如果浏览器没跳回，再从地址栏复制 code= 后面的值，粘贴回来点“换取 Token”。',
      '注意：Box 换 Token 必须带 Client Secret，所以 Box 没有一键授权，只能用自己的应用。',
    ],
  },
  pcloud: {
    title: '去哪里拿 Client ID / Secret（pCloud 控制台）',
    steps: [
      '打开 docs.pcloud.com，点 Create App（要先登录 pCloud 账号），名字例如 ZyDrive。',
      'Redirect URIs 里加一行 http://localhost:57104（带端口，固定端口 57104）。',
      '建好后拿到 Client ID 和 Client Secret，填回来。',
      '先选好账号所在区再生成链接：自动会先按美区授权；如果你的账号在欧洲，换 Token 时会失败，改选“欧洲”再重新生成链接。',
      '点“生成授权链接”，在浏览器里登录 pCloud 点允许；授权完浏览器会自动跳回 ZyDrive，code 自己换成 Token，不用复制。如果浏览器没跳回，再从地址栏复制 code= 后面的值，粘贴回来点“换取 Token”。',
      '注意：pCloud 换 Token 必须带 Client Secret，所以 pCloud 没有一键授权，只能用自己的应用。',
    ],
  },
}

// OAuthCodeFlow — the one mechanical piece the three OAuth
// providers genuinely share: generate the authorize link, open the
// browser, wait for the jump-back, exchange automatically, then
// test the connection right here in the component. The main flow
// shows exactly one button + status text; the code box, the manual
// exchange button and the token paste live in the parent's
// collapsed "手动粘贴" entry (fallback only). Everything around it
// (field labels, app-console guide, scopes, paste hints) is
// per-provider in the branches below, so the three step-2 forms no
// longer look — or read — like the same form.
//
// Status flow (2026-11-25 redesign): idle → waiting （等浏览器
// 返回…) → exchanging （换取中，inside oauthMsg) → testing （正在
// 测试连接…) → passed （测试通过，下一步可用） / failed （原因 +
// “重新测试”）. A network-kind exchange failure shows one error +
// one "重新换取（不用重新授权）" retry; a dead code gets no retry,
// only a new link.
function OAuthCodeFlow({
  authUrl,
  popupBlocked,
  onOpenAuthUrl,
  oauthBusy,
  oauthMsg,
  waiting,
  onCancelWait,
  genAuthLink,
  generateLabel,
  generatePrimary,
  linkAt,
  authTest,
  authTestMsg,
  onRetest,
  exchangeRetryable,
  onRetryExchange,
}: {
  authUrl: string
  popupBlocked: boolean
  onOpenAuthUrl: () => void
  oauthBusy: boolean
  oauthMsg: string | null
  waiting: boolean
  onCancelWait: () => void
  genAuthLink: () => void
  // One-click card: the same button, relabelled as the card's
  // primary action ("打开浏览器授权").
  generateLabel?: string
  generatePrimary?: boolean
  // linkAt: when the current link was minted (ms epoch); the
  // code lives 10 minutes and is single-use.
  linkAt?: number | null
  authTest: 'idle' | 'testing' | 'passed' | 'failed'
  authTestMsg: string | null
  onRetest: () => void
  exchangeRetryable: boolean
  onRetryExchange: () => void
}) {
  return (
    <>
      <div style={{ display: 'flex', gap: 8, marginBottom: 12, flexWrap: 'wrap' }}>
        <button className={generatePrimary ? 'btn-primary' : 'btn'} disabled={oauthBusy} onClick={genAuthLink}>
          {oauthBusy ? '生成中…' : (generateLabel ?? '生成授权链接')}
        </button>
        {authUrl && (
          <button className="btn" onClick={onOpenAuthUrl}>
            在浏览器里打开
          </button>
        )}
        {waiting && (
          <button className="btn" onClick={onCancelWait}>
            取消等待
          </button>
        )}
      </div>
      {popupBlocked && authUrl && (
        <div className="notice-box" style={{ marginBottom: 12 }}>
          浏览器拦截了弹出窗口：请点“在浏览器里打开”，或复制下面的链接到浏览器里打开。
          <textarea className="textarea" readOnly rows={3} value={authUrl} onFocus={(e) => e.target.select()} style={{ marginTop: 8 }} />
        </div>
      )}
      {linkAt && (
        <p className="muted" style={{ margin: '0 0 12px' }}>
          链接生成于 {new Date(linkAt).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}，10 分钟内有效；授权码只能用一次，换过就作废了。
        </p>
      )}
      {waiting && (
        <div className="notice-box">
          已打开授权页：等你在浏览器里点允许，拿到 code 会自动换 Token、自动测试连接。
        </div>
      )}
      {oauthMsg && <div className="notice-box">{oauthMsg}</div>}
      {exchangeRetryable && (
        <div style={{ display: 'flex', gap: 8, margin: '12px 0' }}>
          <button className="btn-primary" disabled={oauthBusy} onClick={onRetryExchange}>
            重新换取（不用重新授权）
          </button>
        </div>
      )}
      {authTest !== 'idle' && (
        <div className={authTest === 'passed' ? 'notice-box' : authTest === 'failed' ? 'error-box' : 'notice-box'}>
          {authTest === 'testing' ? '正在测试连接…' : authTestMsg}
          {authTest !== 'testing' && (
            <div style={{ marginTop: 8 }}>
              <button className="btn btn-sm" onClick={onRetest}>
                重新测试
              </button>
            </div>
          )}
        </div>
      )}
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
  reauthMount,
}: {
  onClose: () => void
  onAdded: (m: Mount) => void
  // Re-auth entry (from Settings → 网盘 → 重新授权): open straight at
  // the credentials step for that provider, with the old name/group
  // prefilled. V0.9.0: saving updates the SAME mount in place
  // (UpdateMountCredentials) — jobs, groups and cache stay; no new
  // mount is created and the old one is never deleted.
  initialProvider?: ProviderKind
  initialStep?: 1 | 2
  initialName?: string
  initialGroup?: string
  reauthMount?: Mount
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
  const [code, setCode] = useState('')
  const [oauthBusy, setOauthBusy] = useState(false)
  const [oauthMsg, setOauthMsg] = useState<string | null>(null)
  // In-component connection test (2026-11-25 redesign): after the
  // token lands, the auth component itself probes the root listing
  // and shows the result here. Next is only enabled when the test
  // passed for the CURRENT credentials AND the name is filled.
  const [authTest, setAuthTest] = useState<'idle' | 'testing' | 'passed' | 'failed'>('idle')
  const [authTestMsg, setAuthTestMsg] = useState<string | null>(null)
  const [testedCredKey, setTestedCredKey] = useState<string | null>(null)
  // Network-kind exchange failure: the code is still good, so the
  // main flow offers one "重新换取（不用重新授权）" retry.
  const [exchangeRetryable, setExchangeRetryable] = useState(false)
  // Manual fallback entry (collapsed by default): the code box,
  // exchange button and token paste live here, never in the main
  // flow — the normal path is one click + auto return.
  const [showManual, setShowManual] = useState(false)
  // The auto-opened browser tab was blocked: show the link + a
  // manual "在浏览器里打开" button instead of failing silently.
  const [popupBlocked, setPopupBlocked] = useState(false)
  // Own-app secret: desktop apps are public clients and need no
  // secret — the field only appears when the toggle says so.
  const [needSecret, setNeedSecret] = useState(false)
  // "保存为我的应用" checkbox: save the own app when the link is generated.
  const [saveCustomAppChecked, setSaveCustomAppChecked] = useState(true)
  // When the current authorize link was minted (ms epoch): the
  // code is only good for 10 minutes, and a code is single-use,
  // so the UI shows the time and never lets a dead code be
  // exchanged twice (Dropbox one-click fix, 2026-11-25).
  const [linkAt, setLinkAt] = useState<number | null>(null)
  // (B) builtin one-click client id: null = not loaded yet,
  // '' = official app not registered (self-built flow only).
  const [builtinClientId, setBuiltinClientId] = useState<string | null>(null)
  // (B) which app to authorize with — an explicit two-choice
  // card row, never a fold: 'builtin' = the ZyDrive official
  // app (one-click), 'custom' = the user's own app, stored
  // separately and never overwriting the builtin. Default is
  // builtin; when no builtin is configured it flips to custom.
  const [appSource, setAppSource] = useState<'builtin' | 'custom'>('builtin')
  // The user's own saved app for this provider (null = none
  // saved yet). The secret comes back in memory only, to prefill
  // the form for the exchange; it is never persisted by the
  // frontend anywhere.
  const [customApp, setCustomApp] = useState<CustomOAuthApp | null>(null)
  const [customSavedMsg, setCustomSavedMsg] = useState<string | null>(null)
  const [confirmDeleteCustom, setConfirmDeleteCustom] = useState(false)
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
  // The step the dialog was on before this render: the step-2
  // reset effect below must NOT run when the user comes back
  // from step 3 (confirm) — they only went back to fix the
  // name/group, and wiping the authorization + passed test there
  // would force a full re-authorize for a typo (2026-11-25).
  const prevStepRef = useRef(step)
  // Local
  const [localPath, setLocalPath] = useState('')
  // Drime
  const [workspaceId, setWorkspaceId] = useState('')
  // MEGA (C, one-page form): email + password login, optional 2FA.
  // The password is only ever sent to TestMountConfig /
  // AddMountWithCredentials in memory — it is never stored; the
  // backend keeps the exported session instead (see accounts.go).
  const [megaEmail, setMegaEmail] = useState('')
  const [megaPassword, setMegaPassword] = useState('')
  const [megaTwoFactor, setMegaTwoFactor] = useState('')
  // pCloud region for the OAuth flow: 'auto' probes US first,
  // 'us' / 'eu' pin the data centre. It only affects BeginOAuth
  // (which authorize/token host to use).
  const [pcloudRegion, setPcloudRegion] = useState('auto')
  // Drime workspace picker (V0.9.0): workspaces for the pasted
  // token, fetched from GET /me/workspaces — pick from the list,
  // never type a number. Single workspace auto-selects.
  const [workspaces, setWorkspaces] = useState<import('../mygo').DrimeWorkspace[] | null>(null)
  const [wsLoading, setWsLoading] = useState(false)
  const [wsError, setWsError] = useState<string | null>(null)
  const [wsManual, setWsManual] = useState(false)
  const fetchWorkspaces = async () => {
    const token = accessToken.trim()
    if (!token) {
      setWsError('请先粘贴个人访问令牌，再获取工作区列表。')
      return
    }
    setWsLoading(true)
    setWsError(null)
    try {
      const list = await api.listDrimeWorkspaces(token)
      setWorkspaces(list)
      if (list.length === 1) {
        setWorkspaceId(String(list[0].id))
      } else if (list.length === 0) {
        setWsError('这个令牌下没有工作区。')
      }
    } catch (e) {
      setWsError(friendlyError(e))
    } finally {
      setWsLoading(false)
    }
  }
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
  // ZyDrive official Client ID is configured and whether the user
  // has saved their own app for this provider, and reset any
  // leftover authorize state from a previous provider. Default
  // source is the official app; when no builtin is configured
  // it flips to "自己的应用" so the step is never a dead end.
  useEffect(() => {
    const fromStep = prevStepRef.current
    prevStepRef.current = step
    if (step !== 2 || !isOAuthProvider(provider)) return
    // Coming back from step 3 (confirm) keeps the authorization
    // and the passed test: the user only went back to fix the
    // name/group. The reset below runs only when entering step 2
    // fresh (from step 1, or after picking another provider).
    if (fromStep === 3) return
    stopWaiting(false)
    setAuthUrl('')
    setCode('')
    setOauthMsg(null)
    setAuthTest('idle')
    setAuthTestMsg(null)
    setTestedCredKey(null)
    setExchangeRetryable(false)
    setShowManual(false)
    setPopupBlocked(false)
    setNeedSecret(false)
    setLinkAt(null)
    setBuiltinClientId(null)
    setCustomApp(null)
    setCustomSavedMsg(null)
    setConfirmDeleteCustom(false)
    setAppSource('builtin')
    void api
      .builtinOAuthClientID(provider)
      .then((id) => {
        setBuiltinClientId(id ?? '')
        if (!id) setAppSource('custom')
      })
      .catch(() => {
        setBuiltinClientId('')
        setAppSource('custom')
      })
    void api
      .getCustomOAuthApp(provider)
      .then((c) => {
        setCustomApp(c ?? null)
        // A saved app that carries a secret is a Web-type app:
        // show its Secret field so the exchange still sends it.
        if (c?.hasSecret) setNeedSecret(true)
      })
      .catch(() => setCustomApp(null))
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
    megaEmail: megaEmail.trim() || undefined,
    megaPassword: megaPassword || undefined,
    megaTwoFactor: megaTwoFactor.trim() || undefined,
    pcloudRegion: pcloudRegion || undefined,
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
      case 'box':
      case 'pcloud':
        if (!accessToken.trim() && !refreshToken.trim()) return '请先用授权码换取 Token，或直接粘贴 Access Token / Refresh Token。'
        return null
      case 'mega':
        if (!megaEmail.trim() || !megaPassword) return '请填 MEGA 账号邮箱和密码。'
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
    if (!canGoStep3) {
      setFormError(nextHint ?? '请先完成授权并通过连接测试。')
      return
    }
    setFormError(null)
    setError(null)
    setStep(3)
  }

  // The credential identity the last passed in-component test ran
  // against: the TOKENS only (provider + access/refresh token).
  // The probe (TestMountConfig) connects with the tokens — the
  // Client ID/Secret only matter for the exchange, not for the
  // connection — so switching the app source (builtin ↔ own app)
  // after a pass must NOT void it: the tokens are unchanged and
  // the probe result is still valid. Editing a token does void
  // it: the test result belongs to the old tokens.
  const currentCredKey = JSON.stringify({
    provider,
    accessToken: accessToken.trim(),
    refreshToken: refreshToken.trim(),
  })
  const testFresh = authTest === 'passed' && testedCredKey !== null && testedCredKey === currentCredKey
  // Next is only enabled when the in-component test passed for the
  // current credentials AND the name is filled (group optional).
  // When it is not, the hint names exactly what is missing.
  const nextHint: string | null = !displayName.trim()
    ? '还差一步：请填写名字。'
    : !testFresh
      ? authTest === 'failed'
        ? '还差一步：连接测试未通过，请看上面的原因，修好后点“重新测试”。'
        : authTest === 'testing'
          ? '正在测试连接，请稍候…'
          : authTest === 'idle'
            ? accessToken.trim() || refreshToken.trim()
              ? '还差一步：已有 Token，展开下面的“手动粘贴”点“测试连接”，通过后就能下一步。'
              : '还差一步：请先完成授权，拿到 Token 后会自动测试连接。'
            : '还差一步：凭证改了，请点“重新测试”。'
      : null
  const canGoStep3 = testFresh && displayName.trim() !== ''

  // In-component connection test: probe the root listing with the
  // current credentials and show the result inside the auth
  // component. overrideTokens is for the exchange path, where the
  // tokens were just set and the state has not re-rendered yet.
  const runAuthTest = async (overrideTokens?: { accessToken: string; refreshToken: string }) => {
    if (!provider || !isOAuthProvider(provider)) return
    const at = (overrideTokens?.accessToken ?? accessToken).trim()
    const rt = (overrideTokens?.refreshToken ?? refreshToken).trim()
    if (!at && !rt) {
      setAuthTest('failed')
      setTestedCredKey(null)
      setAuthTestMsg('还没有 Token：请先完成授权，或在下面“手动粘贴”里粘贴 Access Token 后点“测试连接”。')
      return
    }
    setAuthTest('testing')
    setAuthTestMsg(null)
    try {
      await api.testMountConfig({ ...buildInput(), accessToken: at || undefined, refreshToken: rt || undefined })
      setAuthTest('passed')
      // Tokens only (see currentCredKey): the app source may
      // change after a pass without voiding it.
      setTestedCredKey(JSON.stringify({ provider, accessToken: at, refreshToken: rt }))
      setAuthTestMsg('连接测试通过：根目录能列出来，可以继续了。')
    } catch (e) {
      setAuthTest('failed')
      setTestedCredKey(null)
      setAuthTestMsg(friendlyError(e))
    }
  }

  // Save: new mount, or — in re-auth mode — new credentials on the
  // SAME mount (jobs, groups and cache stay).
  const saveMount = async (input: AddMountInput): Promise<Mount | null> => {
    if (reauthMount) return api.updateMountCredentials(reauthMount.id, input)
    return api.addMount(input)
  }

  const testAndSave = async () => {
    setTesting(true)
    setError(null)
    try {
      // No second probe here: the connection was already tested
      // in-component (step 2) and the backend remembers the pass
      // for this exact config — skipProbe saves it Verified.
      const m = await saveMount({ ...buildInput(), skipProbe: true })
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
      const m = await saveMount({ ...buildInput(), skipProbe: !fresh })
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
        // NOTE: stopWaiting(false) on purpose — cancelling the
        // remote state here would delete the verifier the pasted
        // code still needs, and the paste would die with a
        // misleading "链接已失效" (Dropbox one-click fix,
        // 2026-11-25). Full cancel only happens on dialog close.
        stopWaiting(false)
        setOauthMsg('等了 5 分钟，浏览器还没有跳回（可能本机回调端口被挡住了）：请点下面的“手动粘贴”，复制地址栏里的 code 粘贴回来，点“换取 Token”。')
        return
      }
      void api
        .pollOAuthCallback(state)
        .then((res) => {
          if (!res) return
          if (res.status === 'done' && res.code) {
            stopWaiting(false)
            setCode(res.code)
            // Pass the state explicitly: the poll callback was
            // created in the render before setOauthState took
            // effect, so the `oauthState` state variable here is
            // STALE ('' on the first flow) and the auto-exchange
            // would die with "链接已失效" (2026-11-25).
            void exchangeCode(res.code, state)
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
    // Own app: remember it when asked, before authorizing — the
    // next add of this provider then fills the Client ID itself.
    // A save failure never blocks the authorization. Box/pCloud
    // ALWAYS need the secret at exchange (no public-client
    // option), so their secret is saved regardless of the
    // needSecret checkbox, which their branches do not show.
    if (appSource === 'custom' && saveCustomAppChecked && clientId.trim()) {
      const secretToSave = provider === 'box' || provider === 'pcloud' ? clientSecret : (needSecret ? clientSecret : '')
      try {
        await api.saveCustomOAuthApp(provider, clientId.trim(), secretToSave)
        setCustomApp({ clientId: clientId.trim(), clientSecret: secretToSave, hasSecret: secretToSave.trim() !== '' })
        setCustomSavedMsg('已保存为“我的应用”，下次添加这个网盘时直接用，不用重填。')
      } catch {
        // Best-effort only: the authorization still proceeds.
      }
    }
    setOauthBusy(true)
    setOauthMsg(null)
    setAuthTest('idle')
    setAuthTestMsg(null)
    setTestedCredKey(null)
    setExchangeRetryable(false)
    setShowManual(false)
    setPopupBlocked(false)
    try {
      // pCloud only: the region picks the authorize/token host
      // (US my/api.pcloud.com vs EU e/eapi.pcloud.com). Every
      // other provider ignores the third argument.
      const region = provider === 'pcloud' ? pcloudRegion : ''
      const start = await api.beginOAuth(provider, id.trim(), region)
      if (!start) throw new Error('生成授权链接失败。')
      // A fresh link voids everything from the previous one: the
      // old code (if any) was issued for the old link's verifier
      // + redirect URI, and exchanging it against the new flow
      // can only fail with invalid_grant. Clear it so the user
      // cannot paste a dead code into the new flow (Dropbox
      // one-click fix, 2026-11-25).
      setCode('')
      setAccessToken('')
      setRefreshToken('')
      setAuthUrl(start.authUrl)
      oauthStateRef.current = start.state
      setLinkClientId(id.trim())
      setLinkClientSecret(secret)
      setLinkAt(Date.now())
      setOauthMsg('已在浏览器里打开授权页：点允许后会自动跳回、自动换 Token、自动测试连接。')
      startWaiting(start.state)
      // One click is the whole flow: open the browser now. If the
      // popup is blocked, show the link + a manual open button
      // instead of leaving the user staring at a dead page.
      const win = window.open(start.authUrl, '_blank', 'noopener')
      if (!win) {
        setPopupBlocked(true)
        setOauthMsg('浏览器拦截了弹出窗口：请点下面的“在浏览器里打开”，授权后会自动跳回。')
      }
    } catch (e) {
      setOauthMsg(friendlyError(e))
    } finally {
      setOauthBusy(false)
    }
  }

  const exchangeCode = async (autoCode?: string, stateOverride?: string) => {
    if (!provider || !isOAuthProvider(provider)) return
    const useCode = (autoCode ?? code).trim()
    if (!useCode) {
      setOauthMsg('请先粘贴授权码（code）。')
      return
    }
    setOauthBusy(true)
    setOauthMsg(null)
    setExchangeRetryable(false)
    try {
      // The exchange uses the Client ID / Secret the link was
      // built with — the one-click flow never fills the form
      // fields, so reading them here would exchange with "".
      const useClientId = linkClientId || clientId.trim()
      const useSecret = linkClientId ? linkClientSecret : clientSecret
      // The state comes from the caller (the poll passes the
      // flow's own state) or the ref — NEVER from the
      // `oauthState` state variable: the poll callback was created
      // in the render before setOauthState took effect, so that
      // variable is stale ('' on the first flow, the previous
      // flow's cancelled state on a retry) and the backend
      // answers "链接已失效" for a perfectly good code
      // (2026-11-25, user report on 0.8.4).
      const useState = stateOverride ?? oauthStateRef.current
      const tokens = await api.exchangeOAuthCode(provider, useClientId, useSecret, useCode, useState, '')
      if (!tokens) throw new Error('换取 Token 失败。')
      setAccessToken(tokens.accessToken)
      if (tokens.refreshToken) setRefreshToken(tokens.refreshToken)
      setOauthMsg(tokens.refreshToken ? 'Token 已换取成功（拿到 Refresh Token，长期有效）。' : 'Token 已换取成功（只拿到 Access Token，过期后需重新授权）。')
      // Auto-test inside the auth component: the exchange just
      // filled the tokens — probe the root listing now, with the
      // fresh tokens, so the result shows here (step 2) and step
      // 3 never tests again.
      await runAuthTest({ accessToken: tokens.accessToken, refreshToken: tokens.refreshToken ?? '' })
    } catch (e) {
      const msg = friendlyError(e)
      setOauthMsg(msg)
      // A network-kind failure leaves the code usable: offer one
      // retry in the main flow ("重新换取，不用重新授权"). A dead
      // code (used / expired / mismatched) or a bad client gets
      // no retry — the only fix is a new link.
      setExchangeRetryable(!/已用过|已过期|不匹配|重新生成链接|重新点“生成授权链接”/.test(msg))
      // A dead code must not linger in the box: if the provider
      // says the code is used / expired / mismatched, the same
      // code can never succeed — clear it so the next attempt
      // cannot be a doomed retry of the same code.
      if (/已用过|已过期|不匹配/.test(msg)) setCode('')
    } finally {
      setOauthBusy(false)
    }
  }

  // Switching the authorization source: drop any half-built
  // authorize link (its Client ID belongs to the other source),
  // but NEVER clear the typed Client ID / Secret — switching back
  // to "官方应用" must keep what the user typed for their own
  // app, so they can switch back and continue (2026-11-25
  // redesign). The builtin official app is untouched by this
  // switch either way. The in-component test is NOT reset here
  // either: it belongs to the tokens, and the tokens survive the
  // switch — voiding the pass on every switch would force a
  // re-authorize for just looking at the other card.
  const pickAppSource = (src: 'builtin' | 'custom') => {
    stopWaiting(false)
    setAuthUrl('')
    setCode('')
    setOauthMsg(null)
    setExchangeRetryable(false)
    setLinkAt(null)
    setCustomSavedMsg(null)
    setConfirmDeleteCustom(false)
    setAppSource(src)
    if (src === 'custom') {
      // Prefill from the saved own app only when the fields are
      // empty — a typed value the user entered beats the saved one.
      if (!clientId.trim() && customApp) {
        setClientId(customApp.clientId)
        setClientSecret(customApp.clientSecret ?? '')
        if (customApp.hasSecret) setNeedSecret(true)
      }
    }
  }

  // Save the form's Client ID / Secret as "我的应用" for this
  // provider (separate store, encrypted Secret). The builtin
  // official app is untouched.
  const saveCustomApp = async () => {
    if (!provider || !isOAuthProvider(provider)) return
    if (!clientId.trim()) {
      setCustomSavedMsg('请先填 Client ID，才能保存为“我的应用”。')
      return
    }
    try {
      await api.saveCustomOAuthApp(provider, clientId.trim(), clientSecret)
      setCustomApp({ clientId: clientId.trim(), clientSecret, hasSecret: clientSecret.trim() !== '' })
      setCustomSavedMsg('已保存为“我的应用”：下次添加这个网盘时，选“自己的应用”就能直接用，不用再填。')
    } catch (e) {
      setCustomSavedMsg(friendlyError(e))
    }
  }

  // Delete the saved own app (two clicks). The builtin official
  // app is untouched — one-click authorization keeps working.
  const deleteCustomApp = async () => {
    if (!provider || !isOAuthProvider(provider)) return
    if (!confirmDeleteCustom) {
      setConfirmDeleteCustom(true)
      return
    }
    try {
      await api.deleteCustomOAuthApp(provider)
      setCustomApp(null)
      setClientId('')
      setClientSecret('')
      setConfirmDeleteCustom(false)
      setCustomSavedMsg('已删除“我的应用”，一键授权（官方应用）不受影响。')
    } catch (e) {
      setCustomSavedMsg(friendlyError(e))
      setConfirmDeleteCustom(false)
    }
  }

  // The Access / Refresh Token fields, shared by both views:
  // the one-click view shows them once the exchange filled them
  // (the user sees what was obtained, and can paste a different
  // token by hand), and the self-built form shows them always.
  // Both renderings bind the same state, so switching views never
  // loses the tokens.
  const tokenFields = (
    <>
      <div className="field">
        <label>Access Token（已通过授权码换取会自动填上；也可直接粘贴）</label>
        <input className="input" type="password" autoComplete="off" value={accessToken} onChange={(e) => setAccessToken(e.target.value)} />
      </div>
      <div className="field">
        <label>Refresh Token（长期有效；有它过期后会自动刷新）</label>
        <input className="input" type="password" autoComplete="off" value={refreshToken} onChange={(e) => setRefreshToken(e.target.value)} />
      </div>
    </>
  )

  // Manual fallback (collapsed by default): the code box, the
  // manual exchange button and the token paste. Only for when the
  // browser did not jump back — the normal flow never needs it.
  // A plain button toggles the div; no <details> triangles.
  // While the poll is waiting for the browser to jump back, the
  // code will exchange itself the moment it lands — a manual
  // paste + click in that window would race it and exchange the
  // same single-use code twice (Dropbox one-click fix,
  // 2026-11-25). Manual exchange is only live once waiting stops
  // (cancelled, timed out, or the jump-back never happened), and
  // once a token was obtained the code is spent anyway.
  const manualDisabled = oauthBusy || waiting || accessToken !== ''
  const manualFallback = (
    <div style={{ marginTop: 12 }}>
      <button className="btn" onClick={() => setShowManual((v) => !v)}>
        {showManual ? '收起手动粘贴' : '手动粘贴（自动跳回失败时用）'}
      </button>
      {showManual && (
        <div style={{ marginTop: 12 }}>
          <div className="field">
            <label>授权码（code，浏览器没自动跳回时才需要手动粘贴）</label>
            <input className="input" placeholder="粘贴 code= 后面的值" autoComplete="off" value={code} disabled={manualDisabled} onChange={(e) => setCode(e.target.value)} />
          </div>
          <div style={{ display: 'flex', gap: 8, marginBottom: 12, flexWrap: 'wrap' }}>
            <button className="btn" disabled={manualDisabled} onClick={() => void exchangeCode()}>
              {oauthBusy ? '换取中…' : waiting ? '等浏览器跳回…' : accessToken !== '' ? '已换到 Token' : '换取 Token'}
            </button>
            <button
              className="btn"
              disabled={authTest === 'testing' || (!accessToken.trim() && !refreshToken.trim())}
              onClick={() => void runAuthTest()}
            >
              {authTest === 'testing' ? '测试中…' : '测试连接'}
            </button>
          </div>
          {tokenFields}
          <p className="muted">
            直接粘贴也可以：已有 Access Token / Refresh Token 的话，粘贴后点“测试连接”，通过后就能下一步。
          </p>
        </div>
      )}
    </div>
  )

  // The self-built app form (B, "自己的应用"): per-provider fields,
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
          {/* The app-console guide is always visible (no triangle
              fold): a user building their own app needs the steps
              in front of them, not hidden behind a click. */}
          <div className="notice-box" style={{ marginBottom: 12 }}>
            <div style={{ marginBottom: 4 }}>{OAUTH_HELP.dropbox.title}</div>
            <ol style={{ margin: '8px 0 0', paddingLeft: 20 }}>
              {OAUTH_HELP.dropbox.steps.map((s, i) => (
                <li key={i} className="muted" style={{ marginBottom: 4 }}>
                  {s}
                </li>
              ))}
            </ol>
            <button className="btn btn-sm" style={{ marginTop: 8 }} onClick={() => window.open('https://github.com/luckzk/zy-drive/blob/main/docs/oauth-apps/dropbox.md', '_blank', 'noopener')}>
              查看完整注册指南
            </button>
          </div>
          <div className="field">
            <label>App key（就是 Client ID，必填）</label>
            <input className="input" autoComplete="off" value={clientId} onChange={(e) => setClientId(e.target.value)} />
          </div>
          <label style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 12 }}>
            <input type="checkbox" checked={needSecret} onChange={(e) => setNeedSecret(e.target.checked)} />
            这个应用需要 Client Secret（仅 Web 应用类型才需要）
          </label>
          {needSecret && (
            <div className="field">
              <label>App secret（Client Secret，加密存在本机）</label>
              <input className="input" type="password" autoComplete="off" value={clientSecret} onChange={(e) => setClientSecret(e.target.value)} />
            </div>
          )}
          <p className="muted">桌面应用是公开客户端，不需要填 Secret：建应用时打开“允许公共客户端”即可。</p>
          <p className="muted">本次授权请求的权限：files.metadata.read、files.content.read、files.content.write、files.metadata.write、account_info.read（最后一项是容量显示要的，在应用的 Permissions 页勾选）。</p>
          <OAuthCodeFlow
            authUrl={authUrl}
            popupBlocked={popupBlocked}
            onOpenAuthUrl={() => window.open(authUrl, '_blank', 'noopener')}
            oauthBusy={oauthBusy}
            oauthMsg={oauthMsg}
            waiting={waiting}
            onCancelWait={() => stopWaiting(false)}
            genAuthLink={() => void genAuthLink(clientId, needSecret ? clientSecret : '')}
            linkAt={linkAt}
            authTest={authTest}
            authTestMsg={authTestMsg}
            onRetest={() => void runAuthTest()}
            exchangeRetryable={exchangeRetryable}
            onRetryExchange={() => void exchangeCode()}
          />
          {manualFallback}
          <p className="muted">
            直接粘贴也可以：Dropbox App Console → 应用 → Settings → 生成的访问令牌（短期，几小时过期）；要长期有效请走上面的授权码流程拿 Refresh Token。
          </p>
        </>
      )}

      {/* Google Drive — its own form: OAuth 客户端 ID / 客户端密钥
          from Google Cloud Console, Drive scope, Playground hint. */}
      {provider === 'drive' && (
        <>
          {/* The app-console guide is always visible (no triangle fold). */}
          <div className="notice-box" style={{ marginBottom: 12 }}>
            <div style={{ marginBottom: 4 }}>{OAUTH_HELP.drive.title}</div>
            <ol style={{ margin: '8px 0 0', paddingLeft: 20 }}>
              {OAUTH_HELP.drive.steps.map((s, i) => (
                <li key={i} className="muted" style={{ marginBottom: 4 }}>
                  {s}
                </li>
              ))}
            </ol>
            <button className="btn btn-sm" style={{ marginTop: 8 }} onClick={() => window.open('https://github.com/luckzk/zy-drive/blob/main/docs/oauth-apps/google-drive.md', '_blank', 'noopener')}>
              查看完整注册指南
            </button>
          </div>
          <div className="field">
            <label>OAuth 客户端 ID（必填）</label>
            <input className="input" autoComplete="off" value={clientId} onChange={(e) => setClientId(e.target.value)} />
          </div>
          <label style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 12 }}>
            <input type="checkbox" checked={needSecret} onChange={(e) => setNeedSecret(e.target.checked)} />
            这个应用需要 Client Secret（仅 Web 应用类型才需要）
          </label>
          {needSecret && (
            <div className="field">
              <label>客户端密钥（Client Secret，加密存在本机）</label>
              <input className="input" type="password" autoComplete="off" value={clientSecret} onChange={(e) => setClientSecret(e.target.value)} />
            </div>
          )}
          <p className="muted">桌面应用是公开客户端，不需要填 Secret：建 OAuth 客户端时选“桌面应用”类型即可。</p>
          <p className="muted">本次授权请求的权限：https://www.googleapis.com/auth/drive（完整访问 Drive；需先在“API 和服务”里启用 Google Drive API）。</p>
          <OAuthCodeFlow
            authUrl={authUrl}
            popupBlocked={popupBlocked}
            onOpenAuthUrl={() => window.open(authUrl, '_blank', 'noopener')}
            oauthBusy={oauthBusy}
            oauthMsg={oauthMsg}
            waiting={waiting}
            onCancelWait={() => stopWaiting(false)}
            genAuthLink={() => void genAuthLink(clientId, needSecret ? clientSecret : '')}
            linkAt={linkAt}
            authTest={authTest}
            authTestMsg={authTestMsg}
            onRetest={() => void runAuthTest()}
            exchangeRetryable={exchangeRetryable}
            onRetryExchange={() => void exchangeCode()}
          />
          {manualFallback}
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
          {/* The app-console guide is always visible (no triangle fold). */}
          <div className="notice-box" style={{ marginBottom: 12 }}>
            <div style={{ marginBottom: 4 }}>{OAUTH_HELP.onedrive.title}</div>
            <ol style={{ margin: '8px 0 0', paddingLeft: 20 }}>
              {OAUTH_HELP.onedrive.steps.map((s, i) => (
                <li key={i} className="muted" style={{ marginBottom: 4 }}>
                  {s}
                </li>
              ))}
            </ol>
            <button className="btn btn-sm" style={{ marginTop: 8 }} onClick={() => window.open('https://github.com/luckzk/zy-drive/blob/main/docs/oauth-apps/onedrive.md', '_blank', 'noopener')}>
              查看完整注册指南
            </button>
          </div>
          <div className="field">
            <label>应用程序（客户端）ID（必填）</label>
            <input className="input" autoComplete="off" value={clientId} onChange={(e) => setClientId(e.target.value)} />
          </div>
          <label style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 12 }}>
            <input type="checkbox" checked={needSecret} onChange={(e) => setNeedSecret(e.target.checked)} />
            这个应用需要 Client Secret（仅 Web 应用类型才需要）
          </label>
          {needSecret && (
            <div className="field">
              <label>Client Secret（加密存在本机）</label>
              <input className="input" type="password" autoComplete="off" value={clientSecret} onChange={(e) => setClientSecret(e.target.value)} />
            </div>
          )}
          <p className="muted">桌面应用是公开客户端，不需要填 Secret：建应用时选“移动和桌面应用程序”平台即可。</p>
          <p className="muted">本次授权请求的权限：Files.ReadWrite.All、offline_access（没有 offline_access 拿不到 Refresh Token）。</p>
          <OAuthCodeFlow
            authUrl={authUrl}
            popupBlocked={popupBlocked}
            onOpenAuthUrl={() => window.open(authUrl, '_blank', 'noopener')}
            oauthBusy={oauthBusy}
            oauthMsg={oauthMsg}
            waiting={waiting}
            onCancelWait={() => stopWaiting(false)}
            genAuthLink={() => void genAuthLink(clientId, needSecret ? clientSecret : '')}
            linkAt={linkAt}
            authTest={authTest}
            authTestMsg={authTestMsg}
            onRetest={() => void runAuthTest()}
            exchangeRetryable={exchangeRetryable}
            onRetryExchange={() => void exchangeCode()}
          />
          {manualFallback}
          <p className="muted">
            直接粘贴也可以：Refresh Token 可在 Microsoft 的 OAuth 授权流程里拿到（权限须含 offline_access）；只有 Access Token 的话过期后需重新授权。
          </p>
        </>
      )}

      {/* Box — its own form: Client ID / Client Secret from the
          Box developer console. Box has NO public-client option:
          the token exchange always needs the secret, so the
          secret field is shown by default (no checkbox) and the
          link is always generated with it. Fixed redirect port
          57103, scope root_readwrite. */}
      {provider === 'box' && (
        <>
          {/* The app-console guide is always visible (no triangle fold). */}
          <div className="notice-box" style={{ marginBottom: 12 }}>
            <div style={{ marginBottom: 4 }}>{OAUTH_HELP.box.title}</div>
            <ol style={{ margin: '8px 0 0', paddingLeft: 20 }}>
              {OAUTH_HELP.box.steps.map((s, i) => (
                <li key={i} className="muted" style={{ marginBottom: 4 }}>
                  {s}
                </li>
              ))}
            </ol>
            <button className="btn btn-sm" style={{ marginTop: 8 }} onClick={() => window.open('https://github.com/luckzk/zy-drive/blob/main/docs/oauth-apps/box.md', '_blank', 'noopener')}>
              查看完整注册指南
            </button>
          </div>
          <div className="field">
            <label>Client ID（必填）</label>
            <input className="input" autoComplete="off" value={clientId} onChange={(e) => setClientId(e.target.value)} />
          </div>
          <div className="field">
            <label>Client Secret（必填，加密存在本机）</label>
            <input className="input" type="password" autoComplete="off" value={clientSecret} onChange={(e) => setClientSecret(e.target.value)} />
          </div>
          <p className="muted">Box 换 Token 必须带 Client Secret：建应用时选 User Authentication (OAuth 2.0)，Redirect URI 填 http://localhost:57103。</p>
          <p className="muted">本次授权请求的权限：root_readwrite（读写 Box 里的全部文件和文件夹）。</p>
          <OAuthCodeFlow
            authUrl={authUrl}
            popupBlocked={popupBlocked}
            onOpenAuthUrl={() => window.open(authUrl, '_blank', 'noopener')}
            oauthBusy={oauthBusy}
            oauthMsg={oauthMsg}
            waiting={waiting}
            onCancelWait={() => stopWaiting(false)}
            genAuthLink={() => void genAuthLink(clientId, clientSecret)}
            linkAt={linkAt}
            authTest={authTest}
            authTestMsg={authTestMsg}
            onRetest={() => void runAuthTest()}
            exchangeRetryable={exchangeRetryable}
            onRetryExchange={() => void exchangeCode()}
          />
          {manualFallback}
          <p className="muted">
            直接粘贴也可以：已有 Access Token / Refresh Token 的话，粘贴后点“测试连接”，通过后就能下一步。
          </p>
        </>
      )}

      {/* pCloud — its own form: Client ID / Client Secret from the
          pCloud console, plus the data-centre region selector.
          Like Box, the exchange always needs the secret, so the
          secret field is shown by default. Fixed redirect port
          57104. Region 'auto' authorizes against the US host
          first; an EU account fails at exchange and must switch
          to 欧洲 and generate a new link. */}
      {provider === 'pcloud' && (
        <>
          {/* The app-console guide is always visible (no triangle fold). */}
          <div className="notice-box" style={{ marginBottom: 12 }}>
            <div style={{ marginBottom: 4 }}>{OAUTH_HELP.pcloud.title}</div>
            <ol style={{ margin: '8px 0 0', paddingLeft: 20 }}>
              {OAUTH_HELP.pcloud.steps.map((s, i) => (
                <li key={i} className="muted" style={{ marginBottom: 4 }}>
                  {s}
                </li>
              ))}
            </ol>
            <button className="btn btn-sm" style={{ marginTop: 8 }} onClick={() => window.open('https://github.com/luckzk/zy-drive/blob/main/docs/oauth-apps/pcloud.md', '_blank', 'noopener')}>
              查看完整注册指南
            </button>
          </div>
          <div className="field">
            <label>Client ID（必填）</label>
            <input className="input" autoComplete="off" value={clientId} onChange={(e) => setClientId(e.target.value)} />
          </div>
          <div className="field">
            <label>Client Secret（必填，加密存在本机）</label>
            <input className="input" type="password" autoComplete="off" value={clientSecret} onChange={(e) => setClientSecret(e.target.value)} />
          </div>
          <div className="field">
            <label>账号所在区</label>
            <select className="input" value={pcloudRegion} onChange={(e) => setPcloudRegion(e.target.value)}>
              <option value="auto">自动（先试美区）</option>
              <option value="us">美国（api.pcloud.com）</option>
              <option value="eu">欧洲（eapi.pcloud.com）</option>
            </select>
          </div>
          <p className="muted">pCloud 账号分美区 / 欧区两个数据中心：自动会先按美区授权；如果你的账号在欧洲，换 Token 时会失败，改选“欧洲”再重新生成链接。换 Token 必须带 Client Secret，Redirect URI 填 http://localhost:57104。</p>
          <OAuthCodeFlow
            authUrl={authUrl}
            popupBlocked={popupBlocked}
            onOpenAuthUrl={() => window.open(authUrl, '_blank', 'noopener')}
            oauthBusy={oauthBusy}
            oauthMsg={oauthMsg}
            waiting={waiting}
            onCancelWait={() => stopWaiting(false)}
            genAuthLink={() => void genAuthLink(clientId, clientSecret)}
            linkAt={linkAt}
            authTest={authTest}
            authTestMsg={authTestMsg}
            onRetest={() => void runAuthTest()}
            exchangeRetryable={exchangeRetryable}
            onRetryExchange={() => void exchangeCode()}
          />
          {manualFallback}
          <p className="muted">
            直接粘贴也可以：已有 Access Token 的话，粘贴后点“测试连接”，通过后就能下一步（pCloud 的 Token 长期有效，没有 Refresh Token）。
          </p>
        </>
      )}
    </>
  )

  return (
    <div className={`dialog-backdrop${closing ? ' closing' : ''}`} onClick={requestClose}>
      <div className={`dialog dialog-wide dialog-flex${closing ? ' closing' : ''}`} onClick={(e) => e.stopPropagation()}>
        <h1>{reauthMount ? `重新授权：${reauthMount.displayName}` : '添加网盘'}</h1>
        <p className="muted" style={{ margin: '0 0 12px' }}>
          {reauthMount
            ? `原地更新凭证：保存后还是这个网盘，同步任务、分组和缓存都在，不会新建一个盘。厂商是固定的（${chosen?.name ?? provider}）。`
            : `第 ${step} 步 / 共 ${totalSteps} 步：${stepLabel}`}
        </p>

        {step === 1 && (
          <>
            {/* The grid lives in .dialog-body: it scrolls on its
                own, the footer stays fixed below it, so the last
                row is never covered by the button bar. */}
            <div className="dialog-body">
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
            <div className="dialog-body">
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
                  <input className="input" type="password" autoComplete="off" value={accessToken} onChange={(e) => { setAccessToken(e.target.value); setWorkspaces(null); setWsError(null) }} />
                </div>
                <div className="field">
                  <label>工作区</label>
                  {!workspaces ? (
                    <>
                      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                        <button className="btn" type="button" disabled={wsLoading || !accessToken.trim()} onClick={() => void fetchWorkspaces()}>
                          {wsLoading ? '获取中…' : '获取工作区列表'}
                        </button>
                        {!accessToken.trim() && <span className="hint" style={{ margin: 0 }}>先粘贴令牌，再点这里拉列表。</span>}
                      </div>
                      {wsError && <div className="error-box" style={{ marginTop: 8 }}>{wsError}</div>}
                    </>
                  ) : (
                    <>
                      {workspaces.map((w) => (
                        <label key={w.id} className="radio-row">
                          <input
                            type="radio"
                            name="drime-workspace"
                            checked={workspaceId === String(w.id)}
                            onChange={() => setWorkspaceId(String(w.id))}
                          />
                          <span>
                            {w.id === 0 ? '个人空间' : w.name}
                            {w.role ? `（${w.role}）` : ''}
                            <span className="muted"> · ID {w.id}</span>
                          </span>
                        </label>
                      ))}
                      {workspaces.length === 1 && <div className="hint">只有一个工作区，已自动选上。</div>}
                      <button className="btn btn-sm" type="button" onClick={() => { setWorkspaces(null); setWsError(null) }}>
                        重新获取
                      </button>
                    </>
                  )}
                  <div style={{ marginTop: 8 }}>
                    <button className="btn btn-sm" type="button" onClick={() => setWsManual(!wsManual)}>
                      {wsManual ? '收起手动输入' : '手动输入 ID'}
                    </button>
                  </div>
                  {wsManual && (
                    <>
                      <input
                        className="input"
                        style={{ marginTop: 8 }}
                        placeholder="例如：8762"
                        autoComplete="off"
                        value={workspaceId}
                        onChange={(e) => setWorkspaceId(e.target.value)}
                      />
                      <div className="hint">只挂个人空间就填 0 或留空；要挂某个团队空间，填它的数字 ID。</div>
                    </>
                  )}
                </div>
              </>
            )}

            {provider === 'mega' && (
              <>
                <div className="notice-box" style={{ marginBottom: 12 }}>
                  用 MEGA 账号的邮箱和密码登录。密码只在本次测试 / 保存时传给后端换会话，保存后只留会话（Session），密码不存本机；重启后用会话直接登录，不用再输密码。开了两步验证的话再填下面那栏。
                </div>
                <div className="field">
                  <label>MEGA 账号邮箱</label>
                  <input className="input" type="email" autoComplete="off" value={megaEmail} onChange={(e) => setMegaEmail(e.target.value)} />
                </div>
                <div className="field">
                  <label>密码</label>
                  <input className="input" type="password" autoComplete="off" value={megaPassword} onChange={(e) => setMegaPassword(e.target.value)} />
                </div>
                <div className="field">
                  <label>两步验证代码（可选，没开 2FA 就留空）</label>
                  <input className="input" inputMode="numeric" autoComplete="off" value={megaTwoFactor} onChange={(e) => setMegaTwoFactor(e.target.value)} />
                </div>
                <p className="muted">MEGA 的文件在上传时就由客户端加密，服务端看不到明文；ZyDrive 通过官方 Go 客户端库（go-mega）接入，不自己写加密协议。</p>
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
            </div>
            <div className="dialog-actions">
              {!reauthMount && (
                <button className="btn" disabled={saving || testState === 'testing'} onClick={() => setStep(1)}>
                  上一步
                </button>
              )}
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
            <div className="dialog-body">
            <div className="field">
              <label>名字</label>
              <input className="input" placeholder="例如：工作 Drive、R2 备份" value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
            </div>
            <div className="field">
              <label>分组（可选）</label>
              <input className="input" placeholder="例如：工作、个人，留空为“未分组”" value={group} onChange={(e) => setGroup(e.target.value)} />
            </div>

            {/* (B) 授权方式二选一：两个并排的选项卡片，与第 1 步
                大图标网格的选中态一致（品牌绿边框 + 淡绿底 +
                勾选标记），没有三角折叠。默认官方应用；没有配
                置官方应用时官方卡片置灰、默认选“自己的应用”。
                切到自己的应用只用单独存的那份，内置官方应用
                不受影响，切回来就恢复。 */}
            <div className="provider-grid" style={{ marginBottom: 12 }}>
              <button
                type="button"
                className={`provider-card${appSource === 'builtin' ? ' selected' : ''}`}
                aria-pressed={appSource === 'builtin'}
                disabled={builtinClientId === ''}
                onClick={() => pickAppSource('builtin')}
              >
                <span style={{ flex: 1, minWidth: 0 }}>
                  <span className="provider-name">官方应用（一键授权）</span>
                  <span className="provider-sub" style={{ display: 'block' }}>
                    {builtinClientId === '' ? '这个网盘的官方应用还没注册好' : '不用自己建应用，点授权就好'}
                  </span>
                </span>
                {appSource === 'builtin' && (
                  <span className="provider-check" aria-label="已选">
                    <IconCheck />
                  </span>
                )}
              </button>
              <button
                type="button"
                className={`provider-card${appSource === 'custom' ? ' selected' : ''}`}
                aria-pressed={appSource === 'custom'}
                onClick={() => pickAppSource('custom')}
              >
                <span style={{ flex: 1, minWidth: 0 }}>
                  <span className="provider-name">自己的应用</span>
                  <span className="provider-sub" style={{ display: 'block' }}>
                    {customApp ? `已保存 ${customApp.clientId}` : '用自己在控制台建的应用，存在本机'}
                  </span>
                </span>
                {appSource === 'custom' && (
                  <span className="provider-check" aria-label="已选">
                    <IconCheck />
                  </span>
                )}
              </button>
            </div>

            {appSource === 'builtin' && builtinClientId !== null && builtinClientId !== '' && (
              <div className="notice-box" style={{ borderColor: 'var(--brand)' }}>
                <div style={{ marginBottom: 4 }}>一键授权（ZyDrive 官方应用）</div>
                <p className="muted" style={{ margin: '0 0 8px' }}>
                  不用自己建应用：点授权，在浏览器里登录点允许，code 会自动跳回、自动换 Token、自动测试连接。
                </p>
                <OAuthCodeFlow
                  authUrl={authUrl}
                  popupBlocked={popupBlocked}
                  onOpenAuthUrl={() => window.open(authUrl, '_blank', 'noopener')}
                  oauthBusy={oauthBusy}
                  oauthMsg={oauthMsg}
                  waiting={waiting}
                  onCancelWait={() => stopWaiting(false)}
                  genAuthLink={() => void genAuthLink(builtinClientId, '')}
                  linkAt={linkAt}
                  authTest={authTest}
                  authTestMsg={authTestMsg}
                  onRetest={() => void runAuthTest()}
                  exchangeRetryable={exchangeRetryable}
                  onRetryExchange={() => void exchangeCode()}
                  generateLabel="打开浏览器授权"
                  generatePrimary
                />
                {manualFallback}
                <p className="muted" style={{ margin: '8px 0 0' }}>
                  用的是 ZyDrive 官方共享应用（公开客户端，PKCE，不带 Secret）。共享应用有被限流或停用的风险（rclone
                  的共享客户端正在被逐步停用），想完全自己掌控就用“自己的应用”。
                </p>
              </div>
            )}
            {appSource === 'builtin' && builtinClientId === '' && (
              <div className="notice-box">
                一键授权还没配置：这个网盘的 ZyDrive 官方应用还没注册好，请选“自己的应用”走授权；注册好后这里会出现一键授权按钮。
              </div>
            )}

            {appSource === 'custom' && (
              <>
                {customApp && (
                  <div className="notice-box" style={{ marginBottom: 12 }}>
                    已保存“我的应用”（Client ID：{customApp.clientId}
                    {customApp.hasSecret ? '，密钥已保存' : ''}）。下面改了字段后点“保存为我的应用”更新；删掉它也不会影响一键授权。
                  </div>
                )}
                {selfBuiltForm}
                <label style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 12 }}>
                  <input type="checkbox" checked={saveCustomAppChecked} onChange={(e) => setSaveCustomAppChecked(e.target.checked)} />
                  生成授权链接时自动保存为我的应用（下次添加这个网盘时直接用，不用重填）
                </label>
                <div style={{ display: 'flex', gap: 8, marginBottom: 12, flexWrap: 'wrap' }}>
                  <button className="btn" onClick={() => void saveCustomApp()}>
                    保存为我的应用
                  </button>
                  {customApp && (
                    <button className="btn" onClick={() => void deleteCustomApp()}>
                      {confirmDeleteCustom ? '确认删除我的应用？' : '删除我的应用'}
                    </button>
                  )}
                </div>
                {customSavedMsg && <div className="notice-box">{customSavedMsg}</div>}
              </>
            )}

            {formError && <div className="error-box">{formError}</div>}
            {nextHint && <p className="muted">{nextHint}</p>}
            <div className="notice-box">凭证只保存在你这台 Mac 上（加密存储），不会上传到任何地方。</div>
            </div>
            <div className="dialog-actions">
              <button className="btn" onClick={() => setStep(1)}>
                上一步
              </button>
              <button className="btn" onClick={requestClose}>
                取消
              </button>
              <button className="btn-primary" disabled={!canGoStep3} onClick={goStep3}>
                下一步
              </button>
            </div>
          </>
        )}

        {step === 3 && chosen && isOAuthProvider(provider) && (
          <>
            <div className="dialog-body">
            <p>
              准备添加 <strong>{displayName.trim() || chosen.name}</strong>（{chosen.name}
              {group.trim() ? ` · 分组“${group.trim()}”` : ''}）。
            </p>
            {provider === 'onedrive' && (
              <p>
                将挂载：OneDrive（Microsoft 账号，个人 / 工作 / 学校账号均可）。
              </p>
            )}
            <p className="muted">连接已在上一步测通过，点“保存”直接保存，不再重复测。</p>
            {testing && (
              <div className="notice-box">
                正在保存网盘，请稍候…
              </div>
            )}
            {error && (
              <>
                <div className="error-box">{error}</div>
                <div style={{ display: 'flex', gap: 8, marginBottom: 12 }}>
                  <button className="btn-primary" disabled={testing} onClick={() => void testAndSave()}>
                    重试保存
                  </button>
                  <button className="btn" disabled={testing} onClick={() => setStep(2)}>
                    返回修改
                  </button>
                </div>
              </>
            )}
            </div>
            <div className="dialog-actions">
              <button className="btn" disabled={testing} onClick={() => setStep(2)}>
                上一步
              </button>
              <button className="btn" disabled={testing} onClick={requestClose}>
                取消
              </button>
              {!error && (
                <button className="btn-primary" disabled={testing} onClick={() => void testAndSave()}>
                  {testing ? '保存中…' : '保存'}
                </button>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  )
}
