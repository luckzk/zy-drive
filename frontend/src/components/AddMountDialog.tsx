// AddMountDialog — the three-step "add a drive" wizard.
//   1. pick a provider (OneDrive / Drime are honestly marked V0.2 and
//      cannot be picked yet — their drivers do not exist)
//   2. fill in credentials (per-provider form)
//   3. test & save: the backend probes the root listing first and
//      only persists when the probe succeeds, so a failed test never
//      saves a broken mount.
//
// Dropbox / Drive auth, in order of preference:
//   a. OAuth Authorization Code flow: paste the app's Client ID /
//      Secret, generate the authorize link, open it in your own
//      browser, paste the `code` back, the backend exchanges it for
//      tokens. No localhost callback is involved (see internal/app/
//      oauth.go for why), and no client credentials ship with the app
//      — each user creates their own app.
//   b. Paste a refresh token / access token directly (fallback).
// Credentials are typed on the user's own Mac, encrypted at rest
// (AES-256-GCM, master key in the config dir), and never uploaded.
import { useState } from 'react'
import type { AddMountInput, Mount, ProviderKind } from '../mygo'
import { api, friendlyError } from '../api/client'
import { useClosing } from './motion'

type ProviderOption = {
  kind: ProviderKind
  name: string
  desc: string
  available: boolean
  note?: string
}

const PROVIDERS: ProviderOption[] = [
  { kind: 's3', name: 'S3 / R2 / 兼容 S3', desc: 'Amazon S3、Cloudflare R2、MinIO 等，用 AccessKey + SecretKey 接入。', available: true },
  { kind: 'dropbox', name: 'Dropbox', desc: '用 OAuth 授权码换取 Token，或直接粘贴 Refresh Token。', available: true },
  { kind: 'drive', name: 'Google Drive', desc: '用 OAuth 授权码换取 Token，或直接粘贴 Refresh Token。', available: true },
  { kind: 'local', name: '本地文件夹', desc: '这台 Mac 上的一个文件夹，浏览和同步都能用。', available: true },
  { kind: 'onedrive', name: 'OneDrive', desc: '驱动在 V0.2 接入。', available: false, note: 'V0.2' },
  { kind: 'drime', name: 'Drime', desc: '驱动在 V0.2 接入。', available: false, note: 'V0.2' },
]

const OAUTH_HELP: Record<string, { title: string; steps: string[] }> = {
  dropbox: {
    title: '去哪里拿 Client ID / Secret（Dropbox App Console）',
    steps: [
      '打开 dropbox.com/developers/apps，点 Create app。',
      '选 Scoped access，权限选 Full Dropbox（或 App folder，只访问它自己的文件夹）。',
      'Permissions 页勾选 files.metadata.read、files.metadata.write、files.content.read、files.content.write。',
      'Settings 页拿到 App key（就是 Client ID）和 App secret；Redirect URIs 里加一行 http://localhost。',
      '点“生成授权链接”，在浏览器里登录 Dropbox 点允许；浏览器可能显示“无法访问 localhost”，这是正常的——从地址栏复制 code= 后面的值，粘贴回来点“换取 Token”。',
    ],
  },
  drive: {
    title: '去哪里拿 Client ID / Secret（Google Cloud Console）',
    steps: [
      '打开 console.cloud.google.com，新建一个项目。',
      '“API 和服务”里启用 Google Drive API。',
      '“凭据” → 创建凭据 → OAuth 客户端 ID，应用类型选“桌面应用”。',
      '拿到 Client ID 和 Client Secret；OAuth 同意屏幕先把你自己的 Google 账号加为测试用户。',
      'Authorized redirect URIs 里加 http://localhost。点“生成授权链接”，在浏览器里登录 Google 点允许；浏览器可能显示“无法访问 localhost”，这是正常的——从地址栏复制 code= 后面的值，粘贴回来点“换取 Token”。',
    ],
  },
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
  // Local
  const [localPath, setLocalPath] = useState('')
  // Step 3
  const [testing, setTesting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [formError, setFormError] = useState<string | null>(null)

  const chosen = PROVIDERS.find((p) => p.kind === provider) ?? null

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
    localPath: localPath.trim() || undefined,
  })

  const validateStep2 = (): string | null => {
    if (!displayName.trim()) return '请给网盘起个名字。'
    switch (provider) {
      case 's3':
        if (!bucket.trim()) return '请填桶名（Bucket）。'
        if (!accessKey.trim() || !secretKey.trim()) return '请填 AccessKey 和 SecretKey。'
        return null
      case 'dropbox':
      case 'drive':
        if (!accessToken.trim() && !refreshToken.trim()) return '请先用授权码换取 Token，或直接粘贴 Access Token / Refresh Token。'
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

  const genAuthLink = async () => {
    if (!provider || (provider !== 'dropbox' && provider !== 'drive')) return
    if (!clientId.trim()) {
      setOauthMsg('请先填 Client ID。')
      return
    }
    setOauthBusy(true)
    setOauthMsg(null)
    try {
      const start = await api.beginOAuth(provider, clientId.trim())
      if (!start) throw new Error('生成授权链接失败。')
      setAuthUrl(start.authUrl)
      setOauthState(start.state)
      setOauthMsg('链接已生成：在浏览器里打开它，授权后把地址栏里的 code 粘贴回来。')
    } catch (e) {
      setOauthMsg(friendlyError(e))
    } finally {
      setOauthBusy(false)
    }
  }

  const exchangeCode = async () => {
    if (!provider || (provider !== 'dropbox' && provider !== 'drive')) return
    if (!code.trim()) {
      setOauthMsg('请先粘贴授权码（code）。')
      return
    }
    setOauthBusy(true)
    setOauthMsg(null)
    try {
      const tokens = await api.exchangeOAuthCode(provider, clientId.trim(), clientSecret, code.trim(), oauthState, '')
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

  return (
    <div className={`dialog-backdrop${closing ? ' closing' : ''}`} onClick={requestClose}>
      <div className={`dialog dialog-wide${closing ? ' closing' : ''}`} onClick={(e) => e.stopPropagation()}>
        <h1>添加网盘</h1>
        <p className="muted" style={{ margin: '0 0 12px' }}>
          第 {step} 步 / 共 3 步：{step === 1 ? '选厂商' : step === 2 ? '填凭证' : '测试并保存'}
        </p>

        {step === 1 && (
          <>
            {PROVIDERS.map((p) => (
              <div
                key={p.kind}
                className="agent-row"
                style={{
                  cursor: p.available ? 'pointer' : 'not-allowed',
                  border: provider === p.kind ? '1px solid var(--hatch-widget-accent, #56E16C)' : undefined,
                  borderRadius: 8,
                }}
                onClick={() => p.available && setProvider(p.kind)}
              >
                <div className="agent-main">
                  <div className="agent-name">
                    {p.name} {p.note ? <span className="badge">{p.note}</span> : null}
                  </div>
                  <div className="agent-meta">{p.desc}</div>
                </div>
                {p.available ? (
                  <button className="btn btn-sm" onClick={() => setProvider(p.kind)}>
                    {provider === p.kind ? '已选' : '选择'}
                  </button>
                ) : (
                  <button className="btn btn-sm" disabled>
                    尚未接入
                  </button>
                )}
              </div>
            ))}
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

        {step === 2 && chosen && (
          <>
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
                  <label>桶内前缀（可选，留空为整个桶）</label>
                  <input className="input" placeholder="例如：photos/" value={prefix} onChange={(e) => setPrefix(e.target.value)} />
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

            {(provider === 'dropbox' || provider === 'drive') && (
              <>
                <details style={{ marginBottom: 12 }}>
                  <summary style={{ cursor: 'pointer' }}>{OAUTH_HELP[provider].title}</summary>
                  <ol style={{ margin: '8px 0', paddingLeft: 20 }}>
                    {OAUTH_HELP[provider].steps.map((s, i) => (
                      <li key={i} className="muted" style={{ marginBottom: 4 }}>
                        {s}
                      </li>
                    ))}
                  </ol>
                </details>
                <div className="field">
                  <label>OAuth 应用的 Client ID</label>
                  <input className="input" autoComplete="off" value={clientId} onChange={(e) => setClientId(e.target.value)} />
                </div>
                <div className="field">
                  <label>OAuth 应用的 Client Secret</label>
                  <input className="input" type="password" autoComplete="off" value={clientSecret} onChange={(e) => setClientSecret(e.target.value)} />
                </div>
                <div style={{ display: 'flex', gap: 8, marginBottom: 12 }}>
                  <button className="btn" disabled={oauthBusy} onClick={() => void genAuthLink()}>
                    {oauthBusy ? '生成中…' : '生成授权链接'}
                  </button>
                </div>
                {authUrl && (
                  <>
                    <div className="field">
                      <label>授权链接（复制到浏览器里打开）</label>
                      <textarea className="textarea" readOnly rows={3} value={authUrl} onFocus={(e) => e.target.select()} />
                    </div>
                    <div className="field">
                      <label>授权码（code，授权后从浏览器地址栏复制）</label>
                      <input className="input" placeholder="粘贴 code= 后面的值" autoComplete="off" value={code} onChange={(e) => setCode(e.target.value)} />
                    </div>
                    <div style={{ display: 'flex', gap: 8, marginBottom: 12 }}>
                      <button className="btn" disabled={oauthBusy} onClick={() => void exchangeCode()}>
                        {oauthBusy ? '换取中…' : '换取 Token'}
                      </button>
                    </div>
                  </>
                )}
                {oauthMsg && <div className="notice-box">{oauthMsg}</div>}
                <div className="field">
                  <label>Access Token（已通过授权码换取会自动填上；也可直接粘贴）</label>
                  <input className="input" type="password" autoComplete="off" value={accessToken} onChange={(e) => setAccessToken(e.target.value)} />
                </div>
                <div className="field">
                  <label>Refresh Token（长期有效；有它过期后会自动刷新）</label>
                  <input className="input" type="password" autoComplete="off" value={refreshToken} onChange={(e) => setRefreshToken(e.target.value)} />
                </div>
                <p className="muted">
                  {provider === 'dropbox'
                    ? '直接粘贴也可以：Dropbox App Console → Settings → 生成的访问令牌（短期，几小时过期）；要长期有效请走上面的授权码流程拿 Refresh Token。'
                    : '直接粘贴也可以：Refresh Token 可在 Google OAuth Playground（需选 Drive 权限、勾选 offline）里拿到；只有 Access Token 的话过期后需重新授权。'}
                </p>
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

        {step === 3 && chosen && (
          <>
            <p>
              准备添加 <strong>{displayName.trim() || chosen.name}</strong>（{chosen.name}
              {group.trim() ? ` · 分组“${group.trim()}”` : ''}）。
            </p>
            <p className="muted">点“测试并保存”后会先连一次网盘根目录，通了才保存；不通不会保存，也不会留下坏网盘。</p>
            {testing && (
              <div className="notice-box">
                正在连接测试{provider === 'local' ? '本地文件夹' : '网盘'}，请稍候…
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
