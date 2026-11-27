// visual-check — 截图自查：写完前端代码后先截图自己看，再推送。
//
// 用法（在仓库根目录）：
//   node scripts/visual-check/check.mjs [shots-dir]
// 默认截图输出到 scripts/visual-check/shots/（已 gitignore，不推送）。
//
// 做法：静态服务跑 frontend/dist（生产构建产物），headless Chromium
// 1280×800 打开（mygo 窗口尺寸）。因为纯浏览器里没有 mygo 运行时，
// 脚本在页面加载前注入一个测试专用的 window.mygo 桩：所有 Bind 调用
// 返回空数据（零网盘、零 Agent、零任务、零传输），只为看布局，不看
// 数据——桩只活在检查脚本里，不进前端代码。另截一张不注桩的图，
// 确认 Bind 失败时的人话错误态也能正常渲染。
import { chromium } from 'playwright-core'
import http from 'node:http'
import { createReadStream, existsSync, mkdirSync } from 'node:fs'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const DIST = path.join(ROOT, 'frontend', 'dist')
const LOCAL_CHROMIUM = path.join(path.dirname(fileURLToPath(import.meta.url)), '.chromium', 'chrome-linux64', 'chrome')
const CHROMIUM = existsSync(LOCAL_CHROMIUM) ? LOCAL_CHROMIUM : '/opt/meta-chromium/chrome'
const SHOTS = path.resolve(process.argv[2] ?? path.join(path.dirname(fileURLToPath(import.meta.url)), 'shots'))
mkdirSync(SHOTS, { recursive: true })

const MIME = { '.html': 'text/html', '.js': 'text/javascript', '.mjs': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png' }
// .mjs 必须给 text/javascript：PDF.js worker 是 module worker，
// 浏览器拒绝非 JS MIME 的 worker 脚本（v0.14.0：没这一项时 PDF
// overlay 永远渲染不出 canvas）。Go 自身的 mime 表本来就有
// .mjs → text/javascript，真机 mygo 资产服务不受影响。

// 测试桩：Bind 返回空数据，让 App 走零网盘空态。只看布局不看数据。
// VISUAL_MOUNTS=1 时注入三条假网盘（仅检查脚本里有，不进前端代码），
// 用来看分组渲染（组名紧挨数量、悬停出编辑/删除）。
const CAPS = {
  canMkdir: true, canRename: true, canDelete: true, deleteToTrash: true,
  supportsServerSideCopy: false, supportsVersions: false, supportsShare: true,
  supportsSearch: true, supportsDelta: true, supportsThumbnail: false,
  supportsResumableUpload: true, supportsEncryption: false, supportsStorageClass: false,
  supportsPresignedUrl: false, idBased: false, maxFileSize: 0, minPartSize: 0, maxPathLen: 0, illegalChars: '',
}
const MOCK_MOUNTS = [
  // S3 根是 ""（provider root），桶名只活在 rootLabel 里作显示；
  // 旧数据 rootRef "test-bucket" 会让浏览列出桶内同名文件夹。
  // endpointHost 决定侧栏品牌 logo：R2 域名 → Cloudflare 标，
  // 其他 → AWS S3 标（display-only，后端从非敏感配置派生）。
  { id: 'm-s3', displayName: '我的 R2', provider: 's3', accountId: 'a1', rootRef: '', rootLabel: 'test-bucket', endpointHost: 'abc123.r2.cloudflarestorage.com', group: '工作', proxyRef: '', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
  { id: 'm-s3aws', displayName: 'AWS 备份', provider: 's3', accountId: 'a4', rootRef: '', rootLabel: 'backup-bucket', endpointHost: 's3.ap-northeast-1.amazonaws.com', group: '存储 / 备份', proxyRef: '', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
  { id: 'm-db', displayName: 'Dropbox', provider: 'dropbox', accountId: 'a2', rootRef: '', rootLabel: '', group: '个人', proxyRef: '', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
  { id: 'm-dr', displayName: '工作 Drive', provider: 'drive', accountId: 'a3', rootRef: '', rootLabel: '', group: '工作', proxyRef: '', caps: { ...CAPS, idBased: true }, status: 'auth_expired', verified: true, createdAt: '', updatedAt: '' },
  { id: 'm-od', displayName: 'OneDrive', provider: 'onedrive', accountId: 'a5', rootRef: '', rootLabel: '', group: '个人', proxyRef: '', caps: { ...CAPS, idBased: true }, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
  { id: 'm-drime', displayName: 'Drime', provider: 'drime', accountId: 'a6', rootRef: '', rootLabel: '', workspaceLabel: '工作区 8762', group: '个人', proxyRef: '', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
  { id: 'm-local', displayName: '本地照片', provider: 'local', accountId: 'a7', rootRef: '/Users/zy/照片', rootLabel: '/Users/zy/照片', group: '存储 / 备份', proxyRef: '', caps: CAPS, status: 'ok', verified: false, createdAt: '', updatedAt: '' },
  // 超长网盘名（30 个汉字）：悬停删除按钮时名字必须仍是单行
  // 省略、不换行；它的容量查询失败，侧栏应显示“容量暂不可用”。
  { id: 'm-long', displayName: '这是一个特别特别长的网盘名字用来测试省略号是否正常工作啊啊啊', provider: 'dropbox', accountId: 'a8', rootRef: '', rootLabel: '', group: '个人', proxyRef: '', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
]
const GB = 1024 ** 3
const TB = 1024 ** 4
// 每网盘的容量桩（仅检查脚本里有，不进前端代码）：Drime
// 291.0 GB / 512.0 GB、OneDrive 236.0 GB / 1.0 TB、S3/R2 不限量
// （没有配额接口）、本地是本机磁盘、超长网盘名那块查询失败。
const STUB_QUOTAS = [
  { mountId: 'm-s3', used: 0, total: 0, unlimited: true, isLocalDisk: false },
  { mountId: 'm-s3aws', used: 0, total: 0, unlimited: true, isLocalDisk: false },
  { mountId: 'm-db', used: 820 * GB, total: 2 * TB, unlimited: false, isLocalDisk: false },
  { mountId: 'm-dr', used: 615 * GB, total: 2 * TB, unlimited: false, isLocalDisk: false },
  { mountId: 'm-od', used: 236 * GB, total: 1 * TB, unlimited: false, isLocalDisk: false },
  { mountId: 'm-drime', used: 291 * GB, total: 512 * GB, unlimited: false, isLocalDisk: false },
  { mountId: 'm-local', used: 400 * GB, total: 1 * TB, unlimited: false, isLocalDisk: true },
  { mountId: 'm-long', used: 0, total: 0, unlimited: false, isLocalDisk: false, error: 'mock quota failure' },
]
// 多类型文件列表（仅检查脚本里有，不进前端代码）：文件夹、文档、
// 表格、演示、图片、视频、音频、压缩包、代码、未知类型各至少一个，
// 用来看文件管理器图标是否按类型区分、是否对齐。
const STUB_NODES = [
  { ref: '照片', path: '/照片', name: '照片', isDir: true, size: 0, modTime: '2026-11-20T10:00:00Z' },
  { ref: '行程.pdf', path: '/行程.pdf', name: '行程.pdf', isDir: false, size: 123456, modTime: '2026-11-20T10:00:00Z' },
  { ref: '预算.xlsx', path: '/预算.xlsx', name: '预算.xlsx', isDir: false, size: 23456, modTime: '2026-11-20T10:00:00Z' },
  { ref: '分享.pptx', path: '/分享.pptx', name: '分享.pptx', isDir: false, size: 345678, modTime: '2026-11-20T10:00:00Z' },
  { ref: '封面.png', path: '/封面.png', name: '封面.png', isDir: false, size: 456789, modTime: '2026-11-20T10:00:00Z' },
  { ref: '旅行.mp4', path: '/旅行.mp4', name: '旅行.mp4', isDir: false, size: 12345678, modTime: '2026-11-20T10:00:00Z' },
  { ref: '录音.m4a', path: '/录音.m4a', name: '录音.m4a', isDir: false, size: 567890, modTime: '2026-11-20T10:00:00Z' },
  { ref: '备份.zip', path: '/备份.zip', name: '备份.zip', isDir: false, size: 9876543, modTime: '2026-11-20T10:00:00Z' },
  { ref: 'main.go', path: '/main.go', name: 'main.go', isDir: false, size: 1234, modTime: '2026-11-20T10:00:00Z' },
  { ref: '笔记.txt', path: '/笔记.txt', name: '笔记.txt', isDir: false, size: 567, modTime: '2026-11-20T10:00:00Z' },
  { ref: '说明.md', path: '/说明.md', name: '说明.md', isDir: false, size: 890, modTime: '2026-11-20T10:00:00Z' },
  { ref: '未知.xyz', path: '/未知.xyz', name: '未知.xyz', isDir: false, size: 890, modTime: '2026-11-20T10:00:00Z' },
  { ref: '合同.docx', path: '/合同.docx', name: '合同.docx', isDir: false, size: 67890, modTime: '2026-11-20T10:00:00Z' },
]
const STUB_MOUNTS = process.env.VISUAL_MOUNTS ? JSON.stringify(MOCK_MOUNTS) : '[]'
const STUB_QUOTAS_JSON = process.env.VISUAL_MOUNTS ? JSON.stringify(STUB_QUOTAS) : '[]'
// VISUAL_SLOW_LOAD=1：ListMounts / MountQuotas 各慢 1200ms 返回，
// 只为断言首屏加载时序（骨架与网盘行绝不同时出现），不进前端代码。
const SLOW_MS = process.env.VISUAL_SLOW_LOAD ? 1200 : 0
const STUB = `(() => {
  const SLOW_MS = ${SLOW_MS}
  // window.open is intercepted in checks: the wizard auto-opens
  // the authorize URL in the browser (2026-11-25 redesign) — in
  // the headless check that would load the real Dropbox authorize
  // page in a new tab. Record the URL and return a fake window so
  // the frontend treats the tab as opened (no popup-blocked
  // fallback) and nothing actually navigates.
  window.__openedUrls = []
  window.open = (url, ...rest) => { window.__openedUrls.push(String(url)); return { closed: false, close: () => {} } }
  // 慢速只影响首次调用：首屏加载时序的断言要看到“加载中”
  // 的样子，但后面的步骤（设置页等）各自调 ListMounts 时
  // 不应再被拖慢，否则固定等待会赶不上。
  let slowLeft = SLOW_MS ? 2 : 0
  const slow = () => { if (slowLeft <= 0) return Promise.resolve(); slowLeft -= 1; return new Promise((r) => setTimeout(r, SLOW_MS)) }
  const empty = { 'DesktopService.ListAgents': [], 'DesktopService.Transfers': [], 'DesktopService.ListSyncJobs': [] }
  // 可变的桩 Agent：SaveAgent 往里加，ListAgents 返回 live 列表
  // （V0.9.0：导出配置包测试需要先有一个 Agent 行）。
  let __mockAgents = []
  // v0.18.0: 可变的桩代理列表（仅检查脚本里有，不进前端代
  // 码）：SaveProxy 新增/改名、DeleteProxy 删除，ListProxies
  // 返回 live 列表；UpdateMountProxy 改 live 网盘的 proxyRef。
  let __mockProxies = [
    { id: 'px-1', name: '家里代理', type: 'http', addr: 'http://127.0.0.1:7890' },
    { id: 'px-2', name: '备用 SOCKS', type: 'socks5', addr: 'socks5://127.0.0.1:1080' },
  ]
  // 可变的桩网盘：ListMounts 返回这份 live 列表，AddMount 往里加、
  // TestMount 把 verified 翻成 true，后面的步骤（设置页重新测试）
  // 才能看到状态变化——静态列表做不到。
  let __mockMounts = ${STUB_MOUNTS}
  const listeners = {}
  window.mygo = {
    locale: 'zh-CN', dir: 'ltr',
    on: (name, fn) => { (listeners[name] ??= []).push(fn); return () => { listeners[name] = (listeners[name] ?? []).filter((f) => f !== fn) } },
    once: (name, fn) => { const wrap = (p) => { fn(p); listeners[name] = (listeners[name] ?? []).filter((f) => f !== wrap) }; (listeners[name] ??= []).push(wrap); return () => {} },
    call: async (method, ...args) => {
      if (method === 'DesktopService.ListMounts') { await slow(); return __mockMounts }
      if (method === 'DesktopService.AgentStatus') return { total: 0, online: 0 }
      // A/B/C 桩：内置一键授权只给 Dropbox 配了 Client ID（Drive /
      // OneDrive 走自建应用）；BeginOAuth 返回一个环回地址，
      // PollOAuthCallback 立刻返回 done，模拟浏览器自动跳回。
      if (method === 'DesktopService.BuiltinOAuthClientID') return args[0] === 'dropbox' ? 'zydrive-builtin-id' : ''
      // 自建应用桩（v0.8.3）：默认没保存过自己的应用
      // （GetCustomOAuthApp 返回 null），保存/删除只记下来、
      // 不报错；向导里“自己的应用”表单为空。
      if (method === 'DesktopService.GetCustomOAuthApp') return null
      if (method === 'DesktopService.SaveCustomOAuthApp') return null
      if (method === 'DesktopService.DeleteCustomOAuthApp') return null
      if (method === 'DesktopService.BeginOAuth') return { authUrl: 'https://www.dropbox.com/oauth2/authorize?client_id=x&response_type=code&redirect_uri=http://localhost:54321&state=stub-state', state: 'stub-state' }
      if (method === 'DesktopService.PollOAuthCallback') return { status: 'done', code: 'stub-code-1' }
      if (method === 'DesktopService.ExchangeOAuthCode') {
        // 真实的 Dropbox 会拒绝空 client_id（invalid_client）；
        // 桩也必须拒绝，否则向导的自动换取用错 Client ID 也测
        // 不出来（2026-11-26：轮询回调的过期闭包里 linkClientId
        // 还是 ''，手动换取读的是新 state，所以能成功）。
        window.__lastExchange = args
        if (!args[1]) throw new Error('stub: empty client_id — Dropbox rejects this')
        return { accessToken: 'stub-access', refreshToken: 'stub-refresh' }
      }
      if (method === 'DesktopService.CancelOAuth') return null
      if (method === 'DesktopService.TestMountConfig') return null
      if (method === 'DesktopService.TestMount') {
        const m = __mockMounts.find((x) => x.id === args[0])
        if (!m) throw new Error('stub: unknown mount')
        m.verified = true
        return { ...m }
      }
      if (method === 'DesktopService.AddMount') {
        const input = args[0]
        const m = { id: 'm-new', displayName: input.displayName || '新网盘', provider: input.provider, accountId: 'a-new', rootRef: '', rootLabel: input.bucket || '', group: input.group || '', proxyRef: input.proxyRef || '', caps: ${JSON.stringify(CAPS)}, status: 'ok', verified: !input.skipProbe, createdAt: '', updatedAt: '' }
        __mockMounts.push(m)
        return m
      }
      if (method === 'DesktopService.RemoveMount') {
        __mockMounts = __mockMounts.filter((x) => x.id !== args[0])
        return null
      }
      if (method === 'DesktopService.MountQuotas') { await slow(); return ${STUB_QUOTAS_JSON} }
      if (method === 'DesktopService.ValidateJob') return null
      if (method === 'DesktopService.Browse') return ${JSON.stringify(STUB_NODES)}
      if (method === 'DesktopService.PreviewSync') {
        // v0.17.1 runNow 忙态测试用：计数 + 可选慢速。慢速只在
        // 测试把 window.__slowPreview 置 true 后才生效，平时不
        // 拖慢编辑器的自动重算。空计划是故意的：runNow 的 0 项
        // 守卫应拦截它、报“没有需要同步的内容”，而不是调 RunSync。
        window.__previewSyncCalls = (window.__previewSyncCalls || 0) + 1
        if (window.__slowPreview) await new Promise((r) => setTimeout(r, 900))
        return { jobId: '', direction: 'copy', items: [], filteredOut: 0, hasDeletes: false }
      }
      if (method === 'DesktopService.SaveSyncJob') return args[0]
      if (method === 'DesktopService.Mkdir') return null
      if (method === 'DesktopService.UploadFiles') return { transfers: [], skipped: [] }
      if (method === 'DesktopService.UpdateMountRoot') {
        const mount = (${STUB_MOUNTS}).find((m) => m.id === args[0]) ?? (${STUB_MOUNTS})[0]
        return { ...mount, rootRef: '', rootLabel: args[1] + (args[2] ? '/' + args[2] : '') }
      }
      if (method === 'DesktopService.GetSettings') return { theme: 'light', language: 'zh', downloadDir: '/tmp/zy-downloads', syncCompare: 'size+time', syncConcurrency: 4, syncSpeedLimitKBps: 0, syncConflict: 'keep-both', proxyMode: 'system', proxyHttp: '', proxySocks5: '', previewCacheLimitMB: 2048, previewCacheClearOnExit: false }
      // V0.9.1 预览桩：PreviewURL 按后缀给直链（图片用 240×120
      // 品牌绿块 PNG data URL，真能渲染、肉眼可见；PDF 用最小合法
      // PDF 的 data URL，真能渲染；视频/音频给 data-URL 的无效字
      // 节，播不出来、走降级文案——断言只认元素存在与降级文案）。注意：桩里不能出现
      // zydrive-preview:// URL——桩浏览器解析不了这个 scheme，
      // 视频/iframe 子帧加载挂起后会卡死整页的真实鼠标输入（实
      // 测：单开视频或单开 PDF 没事，两个都开过再关 Inspector，
      // 连侧栏导航都点不动，刷新页面也恢复不了）；真机上 scheme
      // 已注册，不存在这个问题，代理直链本身由 Go 测试覆盖。
      // PreviewText 给两行文本；系统打开/显示返回假缓存路径；
      // 缓存信息/清理给固定数字（设置页断言用）。
      if (method === 'DesktopService.PreviewURL') {
        const ref = args[1] || ''
        if (/\.png$/i.test(ref)) return { url: 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAPAAAAB4CAIAAABD1OhwAAABhUlEQVR4nO3SQQlDARTEwG+k10qqipquk5p4sBAGRkAOeT6/L2Q88wI4ZGhSDE2KoUkxNCmGJsXQpBiaFEOTYmhSDE2KoUkxNCmGJsXQpBiaFEOTYmhSDE2KoUkxNCmGJsXQpBiaFEOTYmhSDE2KoUkxNCmGJsXQpBiaFEOTYmhSDE2KoUkxNCmGJsXQpBiaFEOTYmhSDE2KoUkxNCmGJsXQpBiaFEOTYmhSDE2KoUkxNCmGJsXQpBiaFEOTYmhSDE2KoUkxNCmGJsXQpBiaFEOTYmhSDE2KoUkxNCmGJsXQpBiaFEOTYmhSDE2KoUkxNCmGJsXQpDzP+wUd+wI4tC+AQ/sCOLQvgEP7Aji0L4BD+wI4tC+AQ/sCOLQvgEP7Aji0L4BD+wI4tC+AQ/sCOLQvgEP7Aji0L4BD+wI4tC+AQ/sCOLQvgEP7Aji0L4BD+wI4tC+AQ/sCOLQvgEP7Aji0L4BD+wI4tC+AQ/sCuPMHtQuZPUOxqZAAAAAASUVORK5CYII=', kind: 'direct', mime: 'image/png', size: 456789 }
        if (/\.mp4$/i.test(ref)) return { url: 'data:video/mp4;base64,AAAA', kind: 'proxy', mime: 'video/mp4', size: 12345678 }
        if (/\.m4a$/i.test(ref)) return { url: 'data:audio/mp4;base64,AAAA', kind: 'proxy', mime: 'audio/mp4', size: 567890 }
        if (/\.pdf$/i.test(ref)) return { url: 'data:application/pdf;base64,JVBERi0xLjQKMSAwIG9iago8PCAvVHlwZSAvQ2F0YWxvZyAvUGFnZXMgMiAwIFIgPj4KZW5kb2JqCjIgMCBvYmoKPDwgL1R5cGUgL1BhZ2VzIC9LaWRzIFszIDAgUl0gL0NvdW50IDEgPj4KZW5kb2JqCjMgMCBvYmoKPDwgL1R5cGUgL1BhZ2UgL1BhcmVudCAyIDAgUiAvTWVkaWFCb3ggWzAgMCAyMDAgMjAwXSAvQ29udGVudHMgNCAwIFIgL1Jlc291cmNlcyA8PCAvRm9udCA8PCAvRjEgNSAwIFIgPj4gPj4gPj4KZW5kb2JqCjQgMCBvYmoKPDwgL0xlbmd0aCAzNiA+PgpzdHJlYW0KQlQgL0YxIDE4IFRmIDIwIDE4MCBUZCAoSGVsbG8pIFRqIEVUCmVuZHN0cmVhbQplbmRvYmoKNSAwIG9iago8PCAvVHlwZSAvRm9udCAvU3VidHlwZSAvVHlwZTEgL0Jhc2VGb250IC9IZWx2ZXRpY2EgPj4KZW5kb2JqCnhyZWYKMCA2CjAwMDAwMDAwMDAgNjU1MzUgZiAKMDAwMDAwMDAwOSAwMDAwMCBuIAowMDAwMDAwMDU4IDAwMDAwIG4gCjAwMDAwMDAxMTUgMDAwMDAgbiAKMDAwMDAwMDI0MSAwMDAwMCBuIAowMDAwMDAwMzI3IDAwMDAwIG4gCnRyYWlsZXIKPDwgL1NpemUgNiAvUm9vdCAxIDAgUiA+PgpzdGFydHhyZWYKMzk3CiUlRU9GCg==', kind: 'proxy', mime: 'application/pdf', size: 123456 }
        return { url: 'zydrive-preview://localhost/preview/stub-file', kind: 'proxy', mime: 'application/octet-stream', size: 1234 }
      }
      if (method === 'DesktopService.PreviewText') {
        const ref = args[1] || ''
        if (/\.go$/i.test(ref)) return { text: 'package main\\n\\n// hello from the stub\\nfunc main() {}\\n', truncated: false, size: 1234 }
        // v0.14.0：Markdown 桩给真标题 + 真列表，断言认渲染结果。
        if (/\.md$/i.test(ref)) return { text: '# 说明\\n\\n这是一份说明。\\n\\n- 第一点\\n- 第二点\\n', truncated: false, size: 890 }
        return { text: 'line one\\nline two\\n', truncated: false, size: 1234 }
      }
      if (method === 'DesktopService.OpenWithSystemApp') return '/Users/zy/Library/Caches/com.luckzk.zy-drive/preview/stubkey_' + (args[1] || 'file').split('/').pop()
      if (method === 'DesktopService.RevealInFinder') return '/Users/zy/Library/Caches/com.luckzk.zy-drive/preview/stubkey_' + (args[1] || 'file').split('/').pop()
      if (method === 'DesktopService.PreviewCacheInfo') return { sizeBytes: 1234567, fileCount: 3, limitMB: 2048 }
      if (method === 'DesktopService.ClearPreviewCache') return { freedBytes: 1234567, fileCount: 3 }
      // V0.9.0 桩：原地更新凭证（同 ID、改名/分组、标已验证）、
      // Drime 工作区列表（个人空间 + sese + 设计组）、批量传
      // 输、重试、Agent 推送/触发/运行记录、配置包导出、Token 保存。
      if (method === 'DesktopService.UpdateMountCredentials') {
        const m = __mockMounts.find((x) => x.id === args[0])
        if (!m) throw new Error('stub: unknown mount')
        const input = args[1] || {}
        if (input.displayName) m.displayName = input.displayName
        if (input.group !== undefined) m.group = input.group
        m.verified = true
        return { ...m }
      }
      if (method === 'DesktopService.ListDrimeWorkspaces') return [
        { id: 0, name: '个人空间', role: '' },
        { id: 8762, name: 'sese', role: 'Workspace Owner' },
        { id: 1234, name: '设计组', role: 'Member' },
      ]
      if (method === 'DesktopService.BatchDownload') return { transfers: [{ id: 't-1', kind: 'download', status: 'queued', srcMount: args[0], srcRef: (args[1] ?? [])[0] ?? '', dstMount: 'local', dstRef: '', done: 0, size: 0, speedBps: 0, createdAt: '', updatedAt: '' }], skipped: [] }
      if (method === 'DesktopService.BatchCopy') return { transfers: [{ id: 't-2', kind: 'copy', status: 'queued', srcMount: args[0], srcRef: (args[1] ?? [])[0] ?? '', dstMount: args[2], dstRef: args[3], done: 0, size: 0, speedBps: 0, createdAt: '', updatedAt: '' }], skipped: [] }
      // v0.10.0 文件操作桩：DeleteNodes/RenameNode/MoveNodes
      // 返回逐项结果（前端按 done/failed 显示）；UpdateMountGroup
      // 记录调用参数并改 live 列表，拖拽测试靠它断言。
      if (method === 'DesktopService.UpdateMountGroup') {
        window.__lastGroupUpdate = args
        const m = __mockMounts.find((x) => x.id === args[0])
        if (m) m.group = args[1]
        return null
      }
      // v0.11.0 排序桩：记录调用参数，并按渲染顺序（分组
      // 默认 sort × 组内数组顺序，未分组最后）把卡片插到
      // (group, index) 处——与前端 reorderMounts / 后端
      // MoveMount 语义一致，松手后重拉看到的顺序才与乐观
      // 更新一致。
      if (method === 'DesktopService.MoveMount') {
        window.__lastMoveMount = args
        const [id, group, index] = args
        const m = __mockMounts.find((x) => x.id === id)
        if (!m) throw new Error('stub: unknown mount')
        const groupOrder = [...new Set(__mockMounts.map((x) => x.group))].sort()
        if (groupOrder.includes('')) groupOrder.push(groupOrder.splice(groupOrder.indexOf(''), 1)[0])
        const renderOrder = groupOrder.flatMap((g) => __mockMounts.filter((x) => x.group === g))
        const rest = renderOrder.filter((x) => x.id !== id)
        let flat = 0
        for (const g of groupOrder) {
          const members = rest.filter((x) => x.group === g)
          if (g === group) { flat += Math.min(index, members.length); break }
          flat += members.length
        }
        rest.splice(flat, 0, { ...m, group })
        __mockMounts = rest
        return { ...m, group }
      }
      if (method === 'DesktopService.DeleteNodes') {
        window.__lastDelete = args
        return { done: args[1], failed: [], transfers: [] }
      }
      if (method === 'DesktopService.RenameNode') {
        const parts = (args[1] || '').split('/').filter(Boolean)
        parts.pop()
        parts.push(args[2])
        return parts.join('/')
      }
      if (method === 'DesktopService.MoveNodes') {
        window.__lastMove = args
        if (args[2] === args[0]) return { done: args[1], failed: [], transfers: [] }
        return { done: [], failed: [], transfers: (args[1] ?? []).map((r, i) => ({ id: 't-mv-' + i, kind: 'copy', status: 'queued', srcMount: args[0], srcRef: r, dstMount: args[2], dstRef: args[3], done: 0, size: 0, speedBps: 0, createdAt: '', updatedAt: '' })) }
      }
      if (method === 'DesktopService.RetryTransfer') return { id: args[0], kind: 'upload', status: 'queued', srcMount: '', srcRef: '', dstMount: '', dstRef: '', done: 0, size: 0, speedBps: 0, createdAt: '', updatedAt: '' }
      if (method === 'DesktopService.RetryFailedTransfers') return 0
      if (method === 'DesktopService.PushJobToAgent') return null
      if (method === 'DesktopService.TriggerAgentJob') return null
      if (method === 'DesktopService.ListJobRuns') return []
      if (method === 'DesktopService.ExportAgentBundle') return { path: '/tmp/zy-drive-agent-bundle-ag-1-20261125-000000.json', skipped: [] }
      if (method === 'DesktopService.SaveAgentToken') return null
      if (method === 'DesktopService.ListAgents') return __mockAgents
      if (method === 'DesktopService.SaveAgent') {
        const ag = { ...(args[0] || {}), id: args[0]?.id || 'ag-1', online: false, jobsRunning: 0 }
        const i = __mockAgents.findIndex((x) => x.id === ag.id)
        if (i >= 0) __mockAgents[i] = ag
        else __mockAgents.push(ag)
        return ag
      }
      if (method === 'DesktopService.RemoveAgent') { __mockAgents = __mockAgents.filter((x) => x.id !== args[0]); return null }
      if (method === 'DesktopService.SetDefaultAgent') { __mockAgents.forEach((x) => { x.default = x.id === args[0] }); return null }
      if (method === 'DesktopService.TestAgent') return { online: true, version: '0.9.0', protocolVersion: '1', message: 'Agent 在线（0.9.0，协议 1）' }
      if (method === 'DesktopService.SaveSettings') return null
      // 网络（v0.8.5）：检测到的系统代理 + 代理测试桩。via 与
      // Detail 的文案照抄后端（internal/app/proxy.go）的格式。
      if (method === 'DesktopService.GetSystemProxy') return { detected: true, http: 'http://127.0.0.1:7890', https: 'http://127.0.0.1:7890', via: '系统代理：http://127.0.0.1:7890' }
      if (method === 'DesktopService.TestProxy') return { ok: true, detail: '经代理 http://127.0.0.1:7890 连通（200 OK，跟随系统代理）' }
      // v0.18.0: 异步启动加载（桩里加载瞬间完成，返回 false）、
      // 代理列表的新增/改名/删除、每盘代理改值（改 live 网盘）。
      if (method === 'DesktopService.MountsLoading') return false
      if (method === 'DesktopService.ListProxies') return __mockProxies
      if (method === 'DesktopService.SaveProxy') {
        const p = { ...(args[0] || {}) }
        if (!p.id) p.id = 'px-' + (__mockProxies.length + 1)
        const i = __mockProxies.findIndex((x) => x.id === p.id)
        if (i >= 0) __mockProxies[i] = p
        else __mockProxies.push(p)
        return p
      }
      if (method === 'DesktopService.DeleteProxy') {
        __mockProxies = __mockProxies.filter((x) => x.id !== args[0])
        return null
      }
      if (method === 'DesktopService.UpdateMountProxy') {
        const m = __mockMounts.find((x) => x.id === args[0])
        if (!m) throw new Error('stub: unknown mount')
        m.proxyRef = args[1]
        return { ...m }
      }
      if (method === 'DesktopService.CheckDownloadDir') return null
      if (method === 'DesktopService.GetVersions') return { backend: '0.3.0', protocol: '1', engine: '0.1.0' }
      // v0.15.0 命令面板桩：按名字子串过滤桩列表（服务端搜索的
      // 样子），返回完整 Node（大小/修改时间都在，面板要显示）。
      if (method === 'DesktopService.Search') {
        const q = String(args[1] ?? '').trim().toLowerCase()
        const nodes = ${JSON.stringify(STUB_NODES)}.filter((n) => n.name.toLowerCase().includes(q))
        return { nodes, localOnly: false }
      }
      // v0.15.0 上传文件桩：原生选择器在无头浏览器里打不开，
      // 直接返回一条已选路径——断言只认“点了按钮 → 弹出目标框”。
      if (method === 'DesktopService.PickFiles') return ['/Users/zy/照片/test.jpg']
      // Open-at-login deliberately throws here: in the dev shell /
      // non-packaged runs mygo cannot manage login items, and the
      // Settings page must show its honest "暂不支持" note. The text
      // mirrors the backend's own error (internal/app/settings.go).
      if (method === 'DesktopService.GetOpenAtLogin') throw new Error('app: 开机启动在当前运行方式下不可用（需要打包后的桌面应用）')
      if (method === 'DesktopService.SetOpenAtLogin') return null
      if (method in empty) return empty[method]
      throw new Error('stub: unimplemented ' + method)
    },
    callWith: async (method, args) => window.mygo.call(method, ...(args ?? [])),
  }
  // 测试用：模拟一次 mygo 文件拖放（FileBrowser 用 onFileDrop 接）。
  window.__fireFileDrop = (paths, x = 700, y = 400) => { (listeners['mygo:file-drop'] ?? []).forEach((fn) => fn({ paths, x, y })) }
})()`

function serve() {
  return new Promise((resolve) => {
    const server = http.createServer(async (req, res) => {
      try {
        const url = decodeURIComponent(new URL(req.url, 'http://x').pathname)
        let file = path.join(DIST, url === '/' ? 'index.html' : url)
        if (!existsSync(file) || file.endsWith('/')) file = path.join(DIST, 'index.html')
        const body = await readFile(file)
        res.writeHead(200, { 'content-type': MIME[path.extname(file)] ?? 'application/octet-stream' })
        res.end(body)
      } catch {
        res.writeHead(404)
        res.end('not found')
      }
    })
    server.listen(0, '127.0.0.1', () => resolve(server))
  })
}

const server = await serve()
const port = server.address().port
const browser = await chromium.launch({
  executablePath: CHROMIUM,
  args: ['--no-sandbox', '--disable-gpu'],
})
const failures = []
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } })
  page.on('pageerror', (e) => failures.push(`pageerror: ${e.message}`))
  await page.addInitScript(STUB)
  await page.goto(`http://127.0.0.1:${port}/`, { waitUntil: 'networkidle' })

  // 0b. 首屏加载时序（v0.8.4，VISUAL_MOUNTS + VISUAL_SLOW_LOAD）：
  // 加载中只有骨架、没有网盘行；加载完成后骨架消失、网盘行
  // 出现。之前侧栏分组列表在 loading 三元之外无条件渲染，
  // 骨架还在上面转、网盘已经出来了。
  if (process.env.VISUAL_MOUNTS && process.env.VISUAL_SLOW_LOAD) {
    await page.waitForTimeout(300)
    const loadingMid = await page.evaluate(() => ({
      sidebarSkeletons: document.querySelectorAll('.sidebar .skeleton').length,
      mainSkeletons: document.querySelectorAll('main .skeleton').length,
      mountCards: document.querySelectorAll('.mount-card').length,
    }))
    console.log('loading mid-flight:', JSON.stringify(loadingMid))
    if (loadingMid.sidebarSkeletons === 0 && loadingMid.mainSkeletons === 0) failures.push('加载中没有骨架（应只显示骨架、不渲染网盘行）')
    if (loadingMid.mountCards !== 0) failures.push(`加载中就出现了 ${loadingMid.mountCards} 张网盘卡片（加载完成前不应渲染已保存的网盘）`)
    await page.screenshot({ path: path.join(SHOTS, '00-loading.png') })
    await page.waitForTimeout(1600)
    const loadingDone = await page.evaluate(() => ({
      sidebarSkeletons: document.querySelectorAll('.sidebar .skeleton').length,
      mountCards: document.querySelectorAll('.mount-card').length,
    }))
    console.log('loading settled:', JSON.stringify(loadingDone))
    if (loadingDone.sidebarSkeletons !== 0) failures.push(`加载完成后侧栏还有 ${loadingDone.sidebarSkeletons} 个骨架（应消失）`)
    if (loadingDone.mountCards === 0) failures.push('加载完成后没有网盘卡片（应出现已保存的网盘）')
  }

  await page.waitForTimeout(800)

  // 0. 顶栏品牌 logo：logo.svg 放在 ZyDrive 文字旁、26px 高且加载成功；
  // favicon 也是它（link[rel=icon] 指向 /logo.svg）。
  const logo = await page.evaluate(() => {
    const img = document.querySelector('img.app-logo')
    if (!img) return { found: false }
    const box = img.getBoundingClientRect()
    const icon = document.querySelector('link[rel="icon"]')
    return {
      found: true,
      src: img.getAttribute('src'),
      loaded: img.complete && img.naturalWidth > 0,
      height: Math.round(box.height),
      width: Math.round(box.width),
      favicon: icon ? icon.getAttribute('href') : null,
    }
  })
  if (!logo.found || !logo.loaded || logo.height !== 26 || logo.src !== '/logo.svg' || logo.favicon !== '/logo.svg') {
    failures.push(`header logo: ${JSON.stringify(logo)}`)
  }

  // 1. 文件浏览页：零网盘空态
  await page.screenshot({ path: path.join(SHOTS, '01-browse-empty.png') })

  // 1b. S3 一页表单（C，零网盘模式）：选 S3、填桶 test-bucket +
  // 前缀 photos，同一页上有“测试连接”和“保存”，没有第 3 步；
  // 点测试连接通过后点保存，向导关闭、侧栏出现新网盘。有网盘
  // 时没有空态添加按钮，这一步跳过（向导本身与模式无关）。
  if (!process.env.VISUAL_MOUNTS) {
    await page.getByRole('button', { name: '添加网盘', exact: true }).click()
    await page.waitForTimeout(400)
    // 1a. 添加网盘第 1 步（原型 Demo v8 .pickgrid）：两列大图标
    // 卡片网格，不是小列表行；每张卡一张大品牌 logo + 名字 +
    // 一行小字，点整张卡选中（绿边框 + 淡绿底）。
    const providerGrid = await page.evaluate(() => {
      const cards = [...document.querySelectorAll('.provider-card')]
      const grid = document.querySelector('.provider-grid')
      const cs = grid ? getComputedStyle(grid) : null
      return {
        count: cards.length,
        columns: cs ? cs.gridTemplateColumns.split(' ').length : null,
        allHaveLogo: cards.every((c) => !!c.querySelector('svg')),
        names: cards.map((c) => c.querySelector('.provider-name')?.textContent?.trim()),
      }
    })
    console.log('provider grid:', JSON.stringify(providerGrid))
    if (providerGrid.count !== 9) failures.push(`添加网盘第 1 步应有 9 张网盘卡片，实测 ${providerGrid.count} 张`)
    if (providerGrid.columns !== 2) failures.push(`添加网盘第 1 步应是两列网格，实测 ${providerGrid.columns} 列`)
    if (!providerGrid.allHaveLogo) failures.push('添加网盘卡片里有卡片没有品牌 logo（SVG）')
    await page.screenshot({ path: path.join(SHOTS, '01a-wizard-providers.png') })
    // 1a2. 第 1 步无遮挡（v0.8.3）：内容区独立滚动、底部栏固
    // 定——把内容区滚到底后，最后一行卡片必须完整可见，且任
    // 何卡片都不能与底部操作栏相交。
    const noOverlap = await page.evaluate(() => {
      const body = document.querySelector('.dialog-body')
      const footer = document.querySelector('.dialog-actions')
      const cards = [...document.querySelectorAll('.provider-card')]
      if (!body || !footer) return { hasBody: !!body, hasFooter: !!footer }
      body.scrollTop = body.scrollHeight
      const bodyRect = body.getBoundingClientRect()
      const footerRect = footer.getBoundingClientRect()
      const last = cards[cards.length - 1]?.getBoundingClientRect()
      return {
        hasBody: true,
        hasFooter: true,
        footerStatic: getComputedStyle(footer).position === 'static',
        atBottom: body.scrollHeight - body.scrollTop - body.clientHeight <= 1,
        lastRowVisible: last ? last.bottom <= bodyRect.bottom + 1 : false,
        intersectingFooter: cards.filter((c) => {
          const r = c.getBoundingClientRect()
          return !(r.bottom <= footerRect.top + 1 || r.top >= footerRect.bottom - 1)
        }).length,
      }
    })
    console.log('wizard step1 no-overlap:', JSON.stringify(noOverlap))
    if (!noOverlap.hasBody) failures.push('添加网盘对话框没有 .dialog-body（内容区应独立滚动）')
    if (!noOverlap.hasFooter) failures.push('添加网盘对话框没有 .dialog-actions（底部操作栏）')
    if (noOverlap.hasBody && noOverlap.footerStatic === false) failures.push('底部操作栏应固定不动（flex 布局），实测仍是 sticky')
    if (noOverlap.hasBody && !noOverlap.atBottom) failures.push('第 1 步内容区滚不到底')
    if (noOverlap.hasBody && !noOverlap.lastRowVisible) failures.push('第 1 步最后一行网盘卡片没有完整可见（被底部栏遮住）')
    if (noOverlap.hasBody && noOverlap.intersectingFooter > 0) failures.push(`第 1 步有 ${noOverlap.intersectingFooter} 张卡片与底部操作栏相交（被遮挡）`)
    await page.getByText('S3 / R2 / 兼容 S3').click()
    await page.waitForTimeout(300)
    const providerSelected = await page.evaluate(() => {
      const card = [...document.querySelectorAll('.provider-card')].find((c) => c.textContent?.includes('S3 / R2'))
      if (!card) return { found: false }
      const cs = getComputedStyle(card)
      return { found: true, selected: card.classList.contains('selected'), borderColor: cs.borderColor }
    })
    console.log('provider selected:', JSON.stringify(providerSelected))
    if (!providerSelected.found) failures.push('找不到 S3 网盘卡片（.provider-card）')
    if (providerSelected.found && !providerSelected.selected) failures.push('点 S3 卡片后没有选中态（.selected）')
    if (providerSelected.found && providerSelected.borderColor !== 'rgb(86, 225, 108)') {
      failures.push(`选中卡片边框应为品牌绿 rgb(86, 225, 108)，实测 ${providerSelected.borderColor}`)
    }
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    await page.waitForTimeout(300)
    const dialogInputs = page.locator('.dialog input')
    await dialogInputs.nth(0).fill('R2 测试')
    await dialogInputs.nth(4).fill('test-bucket')
    await dialogInputs.nth(5).fill('photos')
    await dialogInputs.nth(7).fill('ak-test')
    await dialogInputs.nth(8).fill('sk-test')
    // 前缀字段必须写清“不要把桶名再填一遍”。
    const prefixHint = await page.locator('.dialog .field', { hasText: '桶内前缀' }).textContent()
    console.log('s3 prefix hint:', JSON.stringify(prefixHint))
    if (!prefixHint?.includes('不要把桶名再填一遍')) failures.push('S3 前缀字段没有“不要把桶名再填一遍”的提示')
    // 一页表单：同一页上有测试连接 + 保存，没有第 3 步、没有
    // “下一步：测试”。操作栏是 sticky 底部栏：表单再长，保存
    // 按钮也必须在视口内可见，不用滚到底才找得到。
    const s3Form = await page.locator('.dialog').textContent()
    if (!s3Form?.includes('测试连接')) failures.push('S3 一页表单里没有“测试连接”按钮')
    if (!s3Form?.includes('保存')) failures.push('S3 一页表单里没有“保存”按钮')
    if (s3Form?.includes('下一步：测试')) failures.push('S3 不应再有“下一步：测试”（一页表单，没有第 3 步）')
    if (s3Form?.includes('第 3 步')) failures.push('S3 一页表单里不应出现“第 3 步”')
    const saveBox = await page.getByRole('button', { name: '保存', exact: true }).boundingBox()
    console.log('s3 save button box:', JSON.stringify(saveBox))
    if (!saveBox || saveBox.y + saveBox.height > 800) {
      failures.push('S3 一页表单的“保存”按钮不在视口内（sticky 底部操作栏应让它一直可见）')
    }
    await page.getByRole('button', { name: '测试连接', exact: true }).click()
    await page.waitForTimeout(500)
    const testResult = await page.locator('.dialog').textContent()
    console.log('s3 test result:', JSON.stringify(testResult?.slice(0, 200)))
    if (!testResult?.includes('连接测试通过')) failures.push('点“测试连接”后没有显示“连接测试通过”')
    await page.screenshot({ path: path.join(SHOTS, '01b-wizard-s3-form.png') })
    await page.getByRole('button', { name: '保存', exact: true }).click()
    await page.waitForTimeout(500)
    const dialogGone = (await page.locator('.dialog').count()) === 0
    const sidebarAfterAdd = await page.locator('.sidebar').textContent()
    console.log('s3 saved:', JSON.stringify({ dialogGone, hasNewMount: sidebarAfterAdd?.includes('R2 测试') }))
    if (!dialogGone) failures.push('点“保存”后添加向导没有关闭')
    if (!sidebarAfterAdd?.includes('R2 测试')) failures.push('保存后侧栏没有出现新加的“R2 测试”网盘')
    // 恢复零网盘空态：删掉刚加的测试网盘，后面的 1c/1d/1e/1g
    // 仍从空态的“添加网盘”按钮进向导。
    const newCard = page.locator('.mount-card', { hasText: 'R2 测试' })
    await newCard.hover()
    await page.waitForTimeout(300)
    await newCard.locator('.mount-remove').click()
    await page.waitForTimeout(300)
    await page.getByRole('button', { name: '确认移除', exact: true }).click()
    await page.waitForTimeout(500)
    const emptyAgain = await page.locator('.sidebar').textContent()
    if (!emptyAgain?.includes('还没有网盘')) failures.push('删掉测试网盘后侧栏没有回到零网盘空态')

    // 1c. Drime 向导（V0.6.0 起 PAT-only，V0.9.0 起工作区选择器）：
    // Drime 必须可选（不再标 V0.2），第 2 步只有两样——个人访问
    // 令牌（提示“令牌只显示一次”）和工作区选择器（从列表点选，
    // 不手填数字；“手动输入 ID”只作兜底）；OAuth 相关字段
    // （Client ID、生成授权链接、授权码、Refresh Token）必须
    // 一个都不出现。
    await page.getByRole('button', { name: '添加网盘', exact: true }).click()
    await page.waitForTimeout(400)
    const drimeCard = page.locator('.provider-card', { hasText: 'Drime' }).first()
    const drimeCardText = await drimeCard.textContent()
    console.log('drime card:', JSON.stringify(drimeCardText))
    if (drimeCardText?.includes('V0.2')) failures.push('向导里 Drime 还标着 V0.2（应已可选）')
    await drimeCard.click()
    await page.waitForTimeout(300)
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    await page.waitForTimeout(300)
    const drimeStep2 = await page.locator('.dialog').textContent()
    console.log('drime step2:', JSON.stringify(drimeStep2?.slice(0, 400)))
    if (!drimeStep2?.includes('个人访问令牌')) failures.push('Drime 向导第 2 步没有“个人访问令牌”主入口提示')
    if (!drimeStep2?.includes('令牌只显示一次')) failures.push('Drime 向导第 2 步没有“令牌只显示一次”的提示')
    if (drimeStep2?.includes('Client ID')) failures.push('Drime 向导第 2 步出现了 Client ID（PAT-only，应隐藏全部 OAuth 字段）')
    if (drimeStep2?.includes('生成授权链接')) failures.push('Drime 向导第 2 步出现了“生成授权链接”（PAT-only，应隐藏）')
    if (drimeStep2?.includes('授权码')) failures.push('Drime 向导第 2 步出现了授权码（PAT-only，应隐藏）')
    if (drimeStep2?.includes('Refresh Token')) failures.push('Drime 向导第 2 步出现了 Refresh Token（PAT-only，应隐藏）')
    if (!drimeStep2?.includes('获取工作区列表')) failures.push('Drime 向导第 2 步没有“获取工作区列表”按钮（V0.9.0 工作区选择器）')
    if (!drimeStep2?.includes('手动输入 ID')) failures.push('Drime 向导第 2 步没有“手动输入 ID”兜底（列表拉不到时应能手填）')
    if (drimeStep2?.includes('工作区 ID（可选，留空为个人空间）')) failures.push('Drime 向导第 2 步还留着旧的“工作区 ID”输入框（V0.9.0 应改成列表点选）')
    // (C) Drime 也是一页表单：同一页上有测试连接 + 保存，没有第 3 步。
    if (!drimeStep2?.includes('测试连接')) failures.push('Drime 一页表单里没有“测试连接”按钮')
    if (drimeStep2?.includes('下一步：测试')) failures.push('Drime 不应再有“下一步：测试”（一页表单，没有第 3 步）')
    if (drimeStep2?.includes('第 3 步')) failures.push('Drime 一页表单里不应出现“第 3 步”')
    // 工作区选择器流程：粘贴令牌 → 获取列表 → 出现三个工作区
    // （个人空间 / sese / 设计组）→ 点选 sese（8762）→ 选中态。
    await page.locator('.dialog input[type="password"]').first().fill('stub-drime-token')
    await page.waitForTimeout(200)
    await page.getByRole('button', { name: '获取工作区列表', exact: true }).click()
    await page.waitForTimeout(600)
    const wsPicker = await page.evaluate(() => {
      const d = document.querySelector('.dialog')
      if (!d) return { found: false }
      const text = d.textContent ?? ''
      const radios = [...d.querySelectorAll('input[type="radio"]')].map((r) => ({ checked: r.checked, label: r.closest('label')?.textContent ?? '' }))
      return { found: true, hasPersonal: text.includes('个人空间'), hasSese: text.includes('sese'), hasDesign: text.includes('设计组'), radios }
    })
    console.log('drime workspace picker:', JSON.stringify(wsPicker))
    if (!wsPicker.hasPersonal || !wsPicker.hasSese || !wsPicker.hasDesign) failures.push(`Drime 工作区列表不全（应有个人空间 / sese / 设计组），实测 ${JSON.stringify(wsPicker)}`)
    if (wsPicker.radios.length !== 3) failures.push(`Drime 工作区应为 3 个单选项，实测 ${wsPicker.radios.length} 个`)
    if (wsPicker.radios.length === 3) {
      await page.locator('.dialog input[type="radio"]').nth(1).click()
      await page.waitForTimeout(200)
      const wsChecked = await page.evaluate(() => {
        const d = document.querySelector('.dialog')
        const radios = [...(d?.querySelectorAll('input[type="radio"]') ?? [])]
        return radios.map((r) => r.checked)
      })
      if (JSON.stringify(wsChecked) !== JSON.stringify([false, true, false])) failures.push(`点选 sese 后选中态不对，实测 ${JSON.stringify(wsChecked)}`)
    }
    await page.screenshot({ path: path.join(SHOTS, '01c-wizard-drime.png') })
    await page.locator('.dialog-actions >> text=取消').click()
    await page.waitForTimeout(400)

    // 1e. MEGA 表单（v0.9.2）：三处 UI 修正——测试通过变绿、
    // 凭证说明改问号悬浮提示、输入框焦点绿环左侧不再被裁。
    await page.getByRole('button', { name: '添加网盘', exact: true }).click()
    await page.waitForTimeout(400)
    await page.locator('.provider-card', { hasText: 'MEGA' }).first().click()
    await page.waitForTimeout(300)
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    await page.waitForTimeout(300)
    const megaInputs = page.locator('.dialog input')
    await megaInputs.nth(0).fill('mega002@vlog.gs')
    await megaInputs.nth(2).fill('mega002@vlog.gs')
    await megaInputs.nth(3).fill('stub-mega-password')
    await page.getByRole('button', { name: '测试连接', exact: true }).click()
    await page.waitForTimeout(500)
    const megaPassed = await page.evaluate(() => {
      const d = document.querySelector('.dialog')
      if (!d) return { found: false }
      const box = d.querySelector('.success-box')
      const tip = d.querySelector('.help-tip')
      const bubble = d.querySelector('.help-tip-bubble')
      const cs = box ? getComputedStyle(box) : null
      const text = d.textContent ?? ''
      return {
        found: true,
        hasSuccessBox: !!box,
        successBg: cs ? cs.backgroundColor : null,
        successText: box?.textContent?.trim() ?? null,
        hasHelpTip: !!tip,
        bubbleText: bubble?.textContent?.trim() ?? null,
        hasPassedLine: text.includes('已通过测试，点保存即存为已验证。'),
        // 凭证说明不应再单独占一行灰底长条
        hasStandaloneCredBox: [...d.querySelectorAll('.notice-box')].some((b) => b.textContent?.includes('凭证只保存在你这台 Mac 上')),
      }
    })
    console.log('mega passed state:', JSON.stringify(megaPassed))
    if (!megaPassed.hasSuccessBox) failures.push('MEGA 测试通过后没有绿底 success-box（应为绿色提醒）')
    if (megaPassed.successBg !== 'rgb(240, 253, 244)') failures.push(`success-box 背景应为 rgb(240, 253, 244)，实测 ${megaPassed.successBg}`)
    if (!megaPassed.hasHelpTip) failures.push('MEGA 通过后“已通过测试”文末没有“?”悬浮提示')
    if (megaPassed.bubbleText !== '凭证只保存在你这台 Mac 上（加密存储），不会上传到任何地方。') failures.push(`“?”气泡文字不对，实测 ${JSON.stringify(megaPassed.bubbleText)}`)
    if (!megaPassed.hasPassedLine) failures.push('MEGA 测试通过后没有“已通过测试，点保存即存为已验证。”')
    if (megaPassed.hasStandaloneCredBox) failures.push('凭证说明还单独占一行灰底长条（应已收进“?”气泡）')
    // 悬停“?”：气泡应出现。
    await page.locator('.dialog .help-tip').hover()
    await page.waitForTimeout(300)
    const tipVisible = await page.evaluate(() => {
      const b = document.querySelector('.help-tip-bubble')
      if (!b) return { found: false }
      const cs = getComputedStyle(b)
      return { found: true, opacity: cs.opacity, visibility: cs.visibility }
    })
    console.log('mega help-tip hover:', JSON.stringify(tipVisible))
    if (tipVisible.opacity !== '1' || tipVisible.visibility !== 'visible') failures.push(`悬停“?”后气泡没有出现，实测 ${JSON.stringify(tipVisible)}`)
    await page.screenshot({ path: path.join(SHOTS, '01e-wizard-mega-passed.png') })
    // 焦点绿环不被裁：点邮箱输入框，2px outline 左右都应完整
    // 可见——输入框左缘离 .dialog-body 内容区左缘至少 2px
    // （outline-offset 0，outline 向外 2px），顶缘同理。
    await page.locator('.dialog input[type="email"]').click()
    await page.waitForTimeout(200)
    const focusClip = await page.evaluate(() => {
      const body = document.querySelector('.dialog-body')
      const input = document.querySelector('.dialog input[type="email"]')
      if (!body || !input) return { found: false }
      const b = body.getBoundingClientRect()
      const r = input.getBoundingClientRect()
      const cs = getComputedStyle(input)
      return {
        found: true,
        outlineWidth: cs.outlineWidth,
        outlineColor: cs.outlineColor,
        leftGap: r.left - b.left,
        topGap: r.top - b.top,
      }
    })
    console.log('mega input focus:', JSON.stringify(focusClip))
    if (focusClip.outlineColor !== 'rgb(86, 225, 108)') failures.push(`邮箱输入框焦点 outline 应为品牌绿 rgb(86, 225, 108)，实测 ${focusClip.outlineColor}`)
    if (focusClip.found && focusClip.leftGap < 2) failures.push(`邮箱输入框左缘离内容区左缘只有 ${focusClip.leftGap}px，2px 焦点 outline 左侧会被裁掉`)
    if (focusClip.found && focusClip.topGap < 2) failures.push(`邮箱输入框顶缘离内容区顶缘只有 ${focusClip.topGap}px，2px 焦点 outline 顶部会被裁掉`)
    await page.screenshot({ path: path.join(SHOTS, '01f-wizard-mega-focus.png') })
    await page.locator('.dialog-actions >> text=取消').click()
    await page.waitForTimeout(400)

    // 1d. OneDrive 向导（V0.6.0）：OneDrive 必须可选（不再标
    // V0.2），第 2 步是 OAuth 授权码流程——有 Client ID、
    // “生成授权链接”、Azure 门户的建应用指引和 Refresh Token。
    await page.getByRole('button', { name: '添加网盘', exact: true }).click()
    await page.waitForTimeout(400)
    const odCard = page.locator('.provider-card', { hasText: 'OneDrive' }).first()
    const odCardText = await odCard.textContent()
    console.log('onedrive card:', JSON.stringify(odCardText))
    if (odCardText?.includes('V0.2')) failures.push('向导里 OneDrive 还标着 V0.2（应已可选）')
    await odCard.click()
    await page.waitForTimeout(300)
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    await page.waitForTimeout(300)
    const odStep2 = await page.locator('.dialog').textContent()
    console.log('onedrive step2:', JSON.stringify(odStep2?.slice(0, 400)))
    if (!odStep2?.includes('Client ID')) failures.push('OneDrive 向导第 2 步没有 Client ID')
    if (!odStep2?.includes('生成授权链接')) failures.push('OneDrive 向导第 2 步没有“生成授权链接”按钮')
    if (!odStep2?.includes('Azure')) failures.push('OneDrive 向导第 2 步没有 Azure 门户建应用的指引')
    if (!odStep2?.includes('Refresh Token')) failures.push('OneDrive 向导第 2 步没有 Refresh Token 字段')
    await page.screenshot({ path: path.join(SHOTS, '01d-wizard-onedrive.png') })
    await page.locator('.dialog-actions >> text=取消').click()
    await page.waitForTimeout(400)

    // 1e. Dropbox vs Drive 第 2 步：字段和文案必须按网盘区分，
    // 不能是同一套通用表单。Dropbox 是 App key / App secret +
    // Dropbox App Console 指引 + Dropbox 权限；Drive 是 OAuth
    // 客户端 ID / 客户端密钥 + Google Cloud Console 指引 + Drive
    // 权限。两边互相不许出现对方的字段名和控制台名。
    await page.getByRole('button', { name: '添加网盘', exact: true }).click()
    await page.waitForTimeout(400)
    await page.locator('.provider-card', { hasText: 'Dropbox' }).first().click()
    await page.waitForTimeout(300)
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    await page.waitForTimeout(300)
    // v0.8.3：授权方式是二选一卡片（不是三角折叠），默认官方
    // 应用；自建表单在“自己的应用”卡片后面。先点自己的应用，
    // 再查 Dropbox 的字段。
    const dbxChoice = await page.evaluate(() => {
      const d = document.querySelector('.dialog')
      const cards = [...(d?.querySelectorAll('.provider-card') ?? [])].map((c) => c.textContent?.trim())
      const selected = [...(d?.querySelectorAll('.provider-card.selected') ?? [])].map((c) => c.textContent?.trim())
      return { cards, selected, hasDetails: (d?.querySelectorAll('details') ?? []).length }
    })
    console.log('dropbox choice cards:', JSON.stringify(dbxChoice))
    if (!dbxChoice.cards.some((t) => t?.includes('官方应用（一键授权）'))) failures.push('Dropbox 第 2 步没有“官方应用（一键授权）”选项卡片')
    if (!dbxChoice.cards.some((t) => t?.includes('自己的应用'))) failures.push('Dropbox 第 2 步没有“自己的应用”选项卡片')
    if (!dbxChoice.selected.some((t) => t?.includes('官方应用'))) failures.push('Dropbox 第 2 步默认应选中“官方应用（一键授权）”')
    if (dbxChoice.hasDetails > 0) failures.push(`添加网盘向导里还有 ${dbxChoice.hasDetails} 个 <details> 三角折叠（应已全部删掉）`)
    await page.locator('.provider-card', { hasText: '自己的应用' }).first().click()
    await page.waitForTimeout(300)
    const dbxStep2 = await page.locator('.dialog').textContent()
    console.log('dropbox step2:', JSON.stringify(dbxStep2?.slice(0, 400)))
    if (!dbxStep2?.includes('App key')) failures.push('Dropbox 第 2 步没有自己的字段名“App key”')
    if (!dbxStep2?.includes('Dropbox App Console')) failures.push('Dropbox 第 2 步没有自己的建应用指引（Dropbox App Console）')
    if (!dbxStep2?.includes('files.metadata.read')) failures.push('Dropbox 第 2 步没有自己的权限说明（files.metadata.read）')
    if (dbxStep2?.includes('OAuth 客户端 ID')) failures.push('Dropbox 第 2 步串了 Drive 的字段名“OAuth 客户端 ID”')
    if (dbxStep2?.includes('Google Cloud Console')) failures.push('Dropbox 第 2 步串了 Drive 的指引（Google Cloud Console）')
    // 第 2 步长表单可滚动到底（v0.8.3）：内容区能纵向滚动，
    // 滚到底后最后一项完整可见，“下一步”按钮固定可见。
    const dbxScroll = await page.evaluate(() => {
      const body = document.querySelector('.dialog-body')
      if (!body) return { hasBody: false }
      body.scrollTop = body.scrollHeight
      const items = [...body.querySelectorAll('.field, .notice-box, p')]
      const last = items[items.length - 1]?.getBoundingClientRect()
      const bodyRect = body.getBoundingClientRect()
      const nextBtn = [...document.querySelectorAll('.dialog-actions button')].find((b) => b.textContent?.trim() === '下一步')
      const btnRect = nextBtn?.getBoundingClientRect()
      return {
        hasBody: true,
        scrollable: body.scrollHeight > body.clientHeight,
        atBottom: body.scrollHeight - body.scrollTop - body.clientHeight <= 1,
        lastVisible: last ? last.bottom <= bodyRect.bottom + 1 : false,
        nextBtnInViewport: btnRect ? btnRect.bottom <= 800 && btnRect.top >= 0 : false,
      }
    })
    console.log('dropbox step2 scroll:', JSON.stringify(dbxScroll))
    if (!dbxScroll.hasBody) failures.push('OAuth 第 2 步没有 .dialog-body（内容区应独立滚动）')
    if (dbxScroll.hasBody && !dbxScroll.scrollable) failures.push('Dropbox 第 2 步长表单不能滚动（内容区应能纵向滚动）')
    if (dbxScroll.hasBody && !dbxScroll.atBottom) failures.push('Dropbox 第 2 步内容区滚不到底')
    if (dbxScroll.hasBody && !dbxScroll.lastVisible) failures.push('Dropbox 第 2 步滚到底后最后一项没有完整可见')
    if (dbxScroll.hasBody && !dbxScroll.nextBtnInViewport) failures.push('Dropbox 第 2 步“下一步”按钮不在视口内（底部栏应固定可见）')
    await page.screenshot({ path: path.join(SHOTS, '01e-wizard-dropbox.png') })
    await page.locator('.dialog-actions >> text=上一步').click()
    await page.waitForTimeout(300)
    await page.locator('.provider-card', { hasText: 'Google Drive' }).first().click()
    await page.waitForTimeout(300)
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    await page.waitForTimeout(300)
    const driveStep2 = await page.locator('.dialog').textContent()
    console.log('drive step2:', JSON.stringify(driveStep2?.slice(0, 400)))
    if (!driveStep2?.includes('OAuth 客户端 ID')) failures.push('Drive 第 2 步没有自己的字段名“OAuth 客户端 ID”')
    if (!driveStep2?.includes('Google Cloud Console')) failures.push('Drive 第 2 步没有自己的建应用指引（Google Cloud Console）')
    if (!driveStep2?.includes('https://www.googleapis.com/auth/drive')) failures.push('Drive 第 2 步没有自己的权限说明（Drive scope）')
    if (driveStep2?.includes('App key')) failures.push('Drive 第 2 步串了 Dropbox 的字段名“App key”')
    if (driveStep2?.includes('Dropbox App Console')) failures.push('Drive 第 2 步串了 Dropbox 的指引（Dropbox App Console）')
    await page.screenshot({ path: path.join(SHOTS, '01f-wizard-drive.png') })
    await page.locator('.dialog-actions >> text=取消').click()
    await page.waitForTimeout(400)

    // 1f2. Box 向导（V0.9.1）：Box 必须可选，第 2 步是 OAuth
    // 授权码流程——有 Client ID + Client Secret（Box 换 Token
    // 必须带 Secret，没有公开客户端选项）、Box 开发者控制台
    // 指引和 root_readwrite 权限；没有官方应用，所以“官方应用
    // （一键授权）”卡片置灰、默认选“自己的应用”，并显示
    // “一键授权还没配置”。
    await page.getByRole('button', { name: '添加网盘', exact: true }).click()
    await page.waitForTimeout(400)
    // 注意：hasText 'Box' 会子串匹配到 Dropbox 卡（Playwright
    // hasText 不区分大小写），必须用锚定的正则点名字元素本身。
    await page.locator('.provider-card .provider-name', { hasText: /^Box$/ }).click()
    await page.waitForTimeout(300)
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    await page.waitForTimeout(300)
    const boxStep2 = await page.locator('.dialog').textContent()
    console.log('box step2:', JSON.stringify(boxStep2?.slice(0, 400)))
    if (!boxStep2?.includes('这个网盘的官方应用还没注册好')) failures.push('Box 第 2 步没有“一键授权还没配置”的灰卡提示（这个网盘的官方应用还没注册好）')
    if (!boxStep2?.includes('Client ID')) failures.push('Box 第 2 步没有 Client ID')
    if (!boxStep2?.includes('Client Secret')) failures.push('Box 第 2 步没有 Client Secret（Box 换 Token 必须带 Secret）')
    if (!boxStep2?.includes('Box 开发者控制台')) failures.push('Box 第 2 步没有自己的建应用指引（Box 开发者控制台）')
    if (!boxStep2?.includes('root_readwrite')) failures.push('Box 第 2 步没有自己的权限说明（root_readwrite）')
    if (!boxStep2?.includes('http://localhost:57103')) failures.push('Box 第 2 步没有固定重定向端口 57103 的说明')
    if (boxStep2?.includes('Azure')) failures.push('Box 第 2 步串了 OneDrive 的指引（Azure）')
    await page.screenshot({ path: path.join(SHOTS, '01f2-wizard-box.png') })
    await page.locator('.dialog-actions >> text=取消').click()
    await page.waitForTimeout(400)

    // 1f3. pCloud 向导（V0.9.1）：pCloud 必须可选，第 2 步是
    // OAuth 授权码流程——有 Client ID + Client Secret（同 Box，
    // 换 Token 必须带 Secret）、pCloud 控制台指引和账号所在区
    // 选择器（自动 / 美国 / 欧洲）；同样没有官方应用。
    await page.getByRole('button', { name: '添加网盘', exact: true }).click()
    await page.waitForTimeout(400)
    await page.locator('.provider-card', { hasText: 'pCloud' }).first().click()
    await page.waitForTimeout(300)
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    await page.waitForTimeout(300)
    const pcloudStep2 = await page.locator('.dialog').textContent()
    console.log('pcloud step2:', JSON.stringify(pcloudStep2?.slice(0, 400)))
    if (!pcloudStep2?.includes('这个网盘的官方应用还没注册好')) failures.push('pCloud 第 2 步没有“一键授权还没配置”的灰卡提示（这个网盘的官方应用还没注册好）')
    if (!pcloudStep2?.includes('Client ID')) failures.push('pCloud 第 2 步没有 Client ID')
    if (!pcloudStep2?.includes('Client Secret')) failures.push('pCloud 第 2 步没有 Client Secret（pCloud 换 Token 必须带 Secret）')
    if (!pcloudStep2?.includes('账号所在区')) failures.push('pCloud 第 2 步没有账号所在区选择器')
    if (!pcloudStep2?.includes('http://localhost:57104')) failures.push('pCloud 第 2 步没有固定重定向端口 57104 的说明')
    const pcloudRegion = await page.evaluate(() => {
      // v0.18.0: the dialog now has two selects (the per-mount
      // proxy picker comes first) — find the region one by its
      // 'auto' option instead of taking the first select.
      const sel = [...document.querySelectorAll('.dialog select')].find((s) =>
        [...s.options].some((o) => o.value === 'auto'),
      )
      if (!sel) return { found: false }
      return { found: true, value: sel.value, options: [...sel.options].map((o) => o.textContent?.trim()) }
    })
    console.log('pcloud region select:', JSON.stringify(pcloudRegion))
    if (!pcloudRegion.found) failures.push('pCloud 第 2 步没有区选择下拉框')
    if (pcloudRegion.found && pcloudRegion.value !== 'auto') failures.push(`pCloud 区选择默认应为“自动”，实测 ${pcloudRegion.value}`)
    if (pcloudRegion.found && !pcloudRegion.options?.some((o) => o?.includes('欧洲'))) failures.push('pCloud 区选择里没有“欧洲”选项')
    await page.screenshot({ path: path.join(SHOTS, '01f3-wizard-pcloud.png') })
    await page.locator('.dialog-actions >> text=取消').click()
    await page.waitForTimeout(400)

    // 1f4. MEGA 向导（V0.9.1）：MEGA 必须可选，第 2 步是一页表单
    // （没有第 3 步）——邮箱 + 密码 + 可选的两步验证代码；没有
    // Client ID、没有“生成授权链接”、没有授权码；同一页上有
    // “测试连接”与“保存”。
    await page.getByRole('button', { name: '添加网盘', exact: true }).click()
    await page.waitForTimeout(400)
    await page.locator('.provider-card', { hasText: 'MEGA' }).first().click()
    await page.waitForTimeout(300)
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    await page.waitForTimeout(300)
    const megaStep2 = await page.locator('.dialog').textContent()
    console.log('mega step2:', JSON.stringify(megaStep2?.slice(0, 400)))
    if (!megaStep2?.includes('MEGA 账号邮箱')) failures.push('MEGA 向导第 2 步没有“邮箱”字段')
    if (!megaStep2?.includes('两步验证')) failures.push('MEGA 向导第 2 步没有“两步验证”字段')
    if (megaStep2?.includes('Client ID')) failures.push('MEGA 向导第 2 步出现了 Client ID（邮箱+密码登录，不应有 OAuth 字段）')
    if (megaStep2?.includes('生成授权链接')) failures.push('MEGA 向导第 2 步出现了“生成授权链接”（邮箱+密码登录，不应有）')
    if (!megaStep2?.includes('测试连接')) failures.push('MEGA 一页表单里没有“测试连接”按钮')
    if (megaStep2?.includes('第 3 步')) failures.push('MEGA 一页表单里不应出现“第 3 步”')
    await page.screenshot({ path: path.join(SHOTS, '01f4-wizard-mega.png') })
    await page.locator('.dialog-actions >> text=取消').click()
    await page.waitForTimeout(400)

    // 1g. 一键授权自动回调 + 二选一卡片（v0.8.5 改版）：Dropbox 配了
    // 内置 Client ID（桩），第 2 步是“官方应用（一键授权）”与
    // “自己的应用”两个并排选项卡片（没有三角折叠），默认官方
    // 应用；主流程只剩“打开浏览器授权”一个按钮 + 状态文案——
    // 点它后浏览器自动打开（window.open 被桩拦下，只记 URL）、
    // 桩的 PollOAuthCallback 立刻返回 done，Token 自动换好、
    // 连接在授权组件内自动测过；授权码框、换取按钮、Token 粘贴
    // 框全收进默认折叠的“手动粘贴（自动跳回失败时用）”兜底。
    // “下一步”只在测试通过且名字填好时才可用；第 3 步只剩
    // 确认与保存，不再重复测一次；从第 3 步点“上一步”回来，
    // 授权与测试结果必须还在（不用重新授权）。
    await page.getByRole('button', { name: '添加网盘', exact: true }).click()
    await page.waitForTimeout(400)
    // 1g0. 顶行卡片 hover 不被裁（2026-11-25）：网格顶部留了
    // 8px 内边距，hover 上移 1px 后卡片顶边仍在对话框内容区内。
    await page.hover('.provider-card >> nth=0')
    await page.waitForTimeout(300)
    const hoverTop = await page.evaluate(() => {
      const card = document.querySelector('.provider-card')
      const body = document.querySelector('.dialog-body')
      if (!card || !body) return { found: false }
      const r = card.getBoundingClientRect()
      const bodyTop = body.getBoundingClientRect().top
      return { found: true, cardTop: Math.round(r.top), bodyTop: Math.round(bodyTop), clipped: r.top < bodyTop - 1 }
    })
    console.log('hover top card:', JSON.stringify(hoverTop))
    if (!hoverTop.found) failures.push('hover 检查找不到 provider-card / dialog-body')
    if (hoverTop.found && hoverTop.clipped) failures.push(`顶行卡片 hover 上移后被裁：卡片顶 ${hoverTop.cardTop} < 内容区顶 ${hoverTop.bodyTop}`)
    await page.screenshot({ path: path.join(SHOTS, '01g-hover-top-card.png') })
    await page.locator('.provider-card', { hasText: 'Dropbox' }).first().click()
    await page.waitForTimeout(300)
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    await page.waitForTimeout(500)
    const oneclick = await page.locator('.dialog').textContent()
    if (!oneclick?.includes('一键授权')) failures.push('Dropbox 第 2 步没有一键授权卡片（桩配了内置 Client ID）')
    if (!oneclick?.includes('自己的应用')) failures.push('Dropbox 第 2 步没有“自己的应用”选项卡片')
    if (oneclick?.includes('高级：用自己的应用')) failures.push('自建应用入口还收在“高级”三角折叠里（应已改成二选一卡片）')
    const noDetails = await page.evaluate(() => document.querySelectorAll('.dialog details').length)
    if (noDetails > 0) failures.push(`添加网盘向导里还有 ${noDetails} 个 <details> 三角折叠（应已全部删掉）`)
    await page.getByRole('button', { name: '打开浏览器授权', exact: true }).click()
    await page.waitForTimeout(3000)
    const autoFlow = await page.evaluate(() => {
      const d = document.querySelector('.dialog')
      if (!d) return { found: false }
      const text = d.textContent ?? ''
      const inputs = [...d.querySelectorAll('input')]
      const nextBtn = [...d.querySelectorAll('.dialog-actions button')].find((b) => (b.textContent ?? '').trim() === '下一步')
      return {
        found: true,
        text,
        openedUrls: window.__openedUrls ?? [],
        hasTestPassed: text.includes('连接测试通过'),
        hasTokenMsg: text.includes('Token 已换取成功'),
        hasLinkTime: text.includes('链接生成于'),
        // 手动兜底默认折叠：没展开之前没有 code 输入框。
        hasPasteBox: inputs.some((i) => (i.getAttribute('placeholder') ?? '').includes('code=')),
        nextDisabled: nextBtn ? nextBtn.disabled : null,
        nextHint: text.includes('还差一步：请填写名字。'),
        // 自动换取必须用生成链接时的那个 Client ID（内置官方
        // 应用 ID），不能是空串。
        exchangeClientId: (window.__lastExchange ?? [])[1] ?? null,
      }
    })
    console.log('oneclick auto:', JSON.stringify({ opened: autoFlow.openedUrls, hasTestPassed: autoFlow.hasTestPassed, hasTokenMsg: autoFlow.hasTokenMsg, hasPasteBox: autoFlow.hasPasteBox, nextDisabled: autoFlow.nextDisabled, exchangeClientId: autoFlow.exchangeClientId }))
    if (!autoFlow.found) failures.push('一键授权后对话框不见了')
    if (!autoFlow.openedUrls?.some((u) => u.includes('dropbox.com/oauth2/authorize'))) failures.push('点“打开浏览器授权”后没有自动打开浏览器授权页（window.open 没被调用）')
    if (!autoFlow.hasTokenMsg) failures.push('自动回调后没有显示“Token 已换取成功”')
    if (autoFlow.exchangeClientId !== 'zydrive-builtin-id') failures.push(`自动换取用的 Client ID 不对，应为内置官方应用 ID 'zydrive-builtin-id'，实测 ${JSON.stringify(autoFlow.exchangeClientId)}`)
    if (!autoFlow.hasTestPassed) failures.push('自动换到 Token 后没有在授权组件内自动测试连接（应显示“连接测试通过”）')
    if (!autoFlow.hasLinkTime) failures.push('授权链接旁没有显示生成时间（code 只有 10 分钟有效）')
    if (autoFlow.hasPasteBox) failures.push('手动粘贴框默认是展开的（应默认折叠，只在“手动粘贴”入口里）')
    if (autoFlow.nextDisabled !== true) failures.push('名字没填时“下一步”应禁用（测试通过还不够，名字必填）')
    if (!autoFlow.nextHint) failures.push('“下一步”禁用时没有说明还缺名字（应显示“还差一步：请填写名字。”）')
    // 填名字：“下一步”放行；第 3 步只剩确认与保存（不再重复
    // 测一次）；点“上一步”回来，授权与测试结果必须还在。
    await page.locator('.dialog input').first().fill('Dropbox 测试')
    await page.waitForTimeout(300)
    const nextEnabled = await page.evaluate(() => {
      const b = [...document.querySelectorAll('.dialog-actions button')].find((x) => (x.textContent ?? '').trim() === '下一步')
      return b ? !b.disabled : null
    })
    if (nextEnabled !== true) failures.push('名字填好且测试通过后“下一步”仍禁用')
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    await page.waitForTimeout(400)
    const step3 = await page.locator('.dialog').textContent()
    if (!step3?.includes('连接已在上一步测通过')) failures.push('第 3 步没有说明连接已在上一步测过（不应再重复测一次）')
    if (!step3?.includes('保存')) failures.push('第 3 步没有“保存”按钮')
    await page.getByRole('button', { name: '上一步', exact: true }).click()
    await page.waitForTimeout(300)
    const backToStep2 = await page.evaluate(() => {
      const d = document.querySelector('.dialog')
      if (!d) return { found: false }
      const text = d.textContent ?? ''
      const nextBtn = [...d.querySelectorAll('.dialog-actions button')].find((b) => (b.textContent ?? '').trim() === '下一步')
      return { found: true, stillPassed: text.includes('连接测试通过'), nextDisabled: nextBtn ? nextBtn.disabled : null }
    })
    if (!backToStep2.stillPassed) failures.push('从第 3 步点“上一步”回来，授权组件内的测试通过状态丢了（应保留，不用重新授权）')
    if (backToStep2.nextDisabled !== false) failures.push('从第 3 步回来后“下一步”应仍可用（名字已填、测试已通过）')
    // 手动兜底（默认折叠）：展开后 code 框里是自动收到的 code
    // （已禁用：code 已用过）、换取按钮变成禁用的“已换到
    // Token”、Token 字段里是桩换到的 Token。
    await page.getByRole('button', { name: '手动粘贴（自动跳回失败时用）', exact: true }).click()
    await page.waitForTimeout(300)
    const manual = await page.evaluate(() => {
      const d = document.querySelector('.dialog')
      if (!d) return { found: false }
      const inputs = [...d.querySelectorAll('input')]
      const codeInput = inputs.find((i) => (i.getAttribute('placeholder') ?? '').includes('code='))
      const exBtn = [...d.querySelectorAll('button')].find((b) => (b.textContent ?? '').includes('换取 Token') || (b.textContent ?? '').includes('已换到 Token'))
      return {
        found: true,
        codeValue: codeInput?.value ?? '',
        codeDisabled: codeInput ? codeInput.disabled : null,
        exchangeLabel: exBtn?.textContent ?? '',
        exchangeDisabled: exBtn ? exBtn.disabled : null,
        hasAccess: inputs.some((i) => i.value === 'stub-access'),
        hasRefresh: inputs.some((i) => i.value === 'stub-refresh'),
      }
    })
    console.log('oneclick manual:', JSON.stringify(manual))
    if (manual.codeValue !== 'stub-code-1') failures.push(`手动粘贴框里没有自动收到的 code，实测 ${JSON.stringify(manual.codeValue)}`)
    if (manual.codeDisabled !== true) failures.push('自动换到 Token 后授权码输入框没有禁用')
    if (!manual.exchangeLabel.includes('已换到 Token')) failures.push('自动换到 Token 后手动按钮没有变成“已换到 Token”（同一 code 不能再换一次）')
    if (manual.exchangeDisabled !== true) failures.push('自动换到 Token 后手动“换取 Token”按钮没有禁用（会造成二次换取）')
    if (!manual.hasAccess) failures.push('手动粘贴区里没有自动换到的 Access Token')
    if (!manual.hasRefresh) failures.push('手动粘贴区里没有自动换到的 Refresh Token')
    await page.screenshot({ path: path.join(SHOTS, '01g-wizard-oneclick.png') })
    // 切到自己的应用：填 Client ID，保存为我的应用。
    await page.locator('.provider-card', { hasText: '自己的应用' }).first().click()
    await page.waitForTimeout(300)
    const customForm = await page.locator('.dialog').textContent()
    if (!customForm?.includes('App key')) failures.push('切到“自己的应用”后没有出现自建表单（App key）')
    if (!customForm?.includes('保存为我的应用')) failures.push('“自己的应用”下没有“保存为我的应用”按钮')
    await page.getByRole('button', { name: '保存为我的应用', exact: true }).click()
    await page.waitForTimeout(300)
    const saveEmpty = await page.locator('.dialog').textContent()
    if (!saveEmpty?.includes('请先填 Client ID')) failures.push('空 Client ID 点“保存为我的应用”没有提示先填 Client ID')
    const customInputs = page.locator('.dialog input')
    await customInputs.nth(2).fill('my-dbx-key')
    await page.getByRole('button', { name: '保存为我的应用', exact: true }).click()
    await page.waitForTimeout(300)
    const saveDone = await page.locator('.dialog').textContent()
    if (!saveDone?.includes('已保存为“我的应用”')) failures.push('填了 Client ID 点“保存为我的应用”后没有显示已保存')
    await page.screenshot({ path: path.join(SHOTS, '01h-wizard-custom-app.png') })
    // 切回官方应用：内置一键授权必须还在。
    await page.locator('.provider-card', { hasText: '官方应用（一键授权）' }).first().click()
    await page.waitForTimeout(300)
    const backToBuiltin = await page.locator('.dialog').textContent()
    if (!backToBuiltin?.includes('一键授权（ZyDrive 官方应用）')) failures.push('切回官方应用后一键授权不见了（内置应不受自建应用影响）')
    if (!backToBuiltin?.includes('打开浏览器授权')) failures.push('切回官方应用后没有“打开浏览器授权”按钮')
    if (!backToBuiltin?.includes('连接测试通过')) failures.push('切回官方应用后授权组件内的测试通过状态丢了（测试属于 Token，切换应用来源不应清掉它）')
    await page.locator('.dialog-actions >> text=取消').click()
    await page.waitForTimeout(400)
  }

  // 2. 侧栏 + 菜单（问题 3+4）：网盘标题右边只有一个 + 小按钮；
  // 点开是浮层菜单（新建网盘 / 新建分组），没有显眼的大按钮。
  const sidebarButtons = await page.evaluate(() => {
    const sb = document.querySelector('.sidebar')
    return [...(sb?.querySelectorAll('button') ?? [])].map((b) => b.textContent.trim())
  })
  console.log('sidebar buttons:', JSON.stringify(sidebarButtons))
  if (sidebarButtons.some((t) => t === '+ 新建分组')) failures.push('侧栏里还有显眼的“+ 新建分组”大按钮（应已删掉，入口在 + 菜单里）')
  if (sidebarButtons.some((t) => t === '+ 添加网盘')) failures.push('侧栏里还有全宽“+ 添加网盘”按钮（应已删掉，入口在 + 菜单里）')
  await page.getByRole('button', { name: '添加网盘或分组' }).click()
  await page.waitForTimeout(400)
  const menu = await page.evaluate(() => {
    const m = document.querySelector('.sidebar-menu')
    if (!m) return { found: false }
    const cs = getComputedStyle(m)
    return {
      found: true,
      items: [...m.querySelectorAll('button')].map((b) => b.textContent.trim()),
      animDuration: cs.animationDuration,
      animCurve: cs.animationTimingFunction,
      opacity: parseFloat(cs.opacity),
    }
  })
  console.log('sidebar menu:', JSON.stringify(menu))
  if (!menu.found) failures.push('点侧栏 + 后没有弹出菜单（.sidebar-menu）')
  if (menu.found && JSON.stringify(menu.items) !== JSON.stringify(['新建网盘', '新建分组'])) {
    failures.push(`+ 菜单应只有“新建网盘 / 新建分组”，实测 ${JSON.stringify(menu.items)}`)
  }
  if (menu.found && menu.animDuration !== '0.3s') failures.push(`+ 菜单入场动画应为 300ms，实测 ${menu.animDuration}`)
  if (menu.found && menu.animCurve !== 'cubic-bezier(0.22, 1, 0.36, 1)') failures.push(`+ 菜单入场曲线应为基础曲线，实测 ${menu.animCurve}`)
  await page.screenshot({ path: path.join(SHOTS, '02-sidebar-menu.png') })

  // 2b. 从菜单点“新建分组”：输入框独占一行，“确定”横排、不溢出。
  await page.getByRole('menuitem', { name: '新建分组' }).click()
  await page.waitForTimeout(400)
  const groupCheck = await page.evaluate(() => {
    const btns = [...document.querySelectorAll('.sidebar button')].filter((b) => ['确定', '取消'].includes(b.textContent.trim()))
    const sidebar = document.querySelector('.sidebar')?.getBoundingClientRect()
    return btns.map((b) => {
      const r = b.getBoundingClientRect()
      return { text: b.textContent.trim(), w: Math.round(r.width), h: Math.round(r.height), horizontal: r.width > r.height, overflowSidebar: sidebar ? r.right > sidebar.right + 1 : null }
    })
  })
  console.log('new-group buttons:', JSON.stringify(groupCheck))
  for (const b of groupCheck) {
    if (!b.horizontal) failures.push(`按钮“${b.text}”疑似竖排：${b.w}x${b.h}`)
    if (b.overflowSidebar) failures.push(`按钮“${b.text}”溢出侧栏`)
  }
  await page.screenshot({ path: path.join(SHOTS, '02-new-group-open.png') })
  // 再截一张侧栏局部放大，方便肉眼确认“确定”二字横排
  const sidebar = await page.$('.sidebar')
  if (sidebar) await sidebar.screenshot({ path: path.join(SHOTS, '02b-sidebar-new-group.png') })
  await page.keyboard.press('Escape')

  // 2c. 有网盘时：悬停分组行，编辑/删除按钮应出现（原型要求默认隐藏）
  if (process.env.VISUAL_MOUNTS) {
    await page.hover('.group-row').catch(() => failures.push('找不到分组行（.group-row）'))
    await page.waitForTimeout(300)
    const groupHover = await page.evaluate(() => {
      const row = document.querySelector('.group-row')
      const actions = row?.querySelector('.group-actions')
      const cs = actions ? getComputedStyle(actions) : null
      const name = row?.querySelector('.group-name')?.textContent?.trim()
      const badge = row?.querySelector('.badge')?.textContent?.trim()
      const nameRect = row?.querySelector('.group-name')?.getBoundingClientRect()
      const badgeRect = row?.querySelector('.badge')?.getBoundingClientRect()
      return { name, badge, actionsVisible: cs?.display !== 'none', badgeAdjacent: nameRect && badgeRect ? Math.round(badgeRect.left - nameRect.right) : null }
    })
    console.log('group hover:', JSON.stringify(groupHover))
    if (!groupHover.actionsVisible) failures.push('悬停分组行后编辑/删除按钮没有出现')
    if (groupHover.badgeAdjacent !== null && groupHover.badgeAdjacent > 12) failures.push(`分组数量徽章离组名太远：${groupHover.badgeAdjacent}px`)
    const sidebar2 = await page.$('.sidebar')
    if (sidebar2) await sidebar2.screenshot({ path: path.join(SHOTS, '02c-sidebar-group-hover.png') })
  }

  // 2e. 网盘卡片的悬停操作列（VISUAL_MOUNTS，v0.13.0：刷新容量
  // + 删除共用一个 .mount-hover-actions 容器，平时隐藏，悬停
  // 卡片才出现；两个按钮都是内联 SVG 图标，不是 emoji；hover
  // 变危险红）。容器本身是绝对定位（不占布局）。
  if (process.env.VISUAL_MOUNTS) {
    const mountRemove = await page.evaluate(() => {
      const card = document.querySelector('.mount-card')
      const col = card?.querySelector('.mount-hover-actions')
      const btn = card?.querySelector('.mount-remove')
      const refresh = card?.querySelector('.mount-refresh')
      if (!card || !col || !btn || !refresh) return { found: false }
      const cs = getComputedStyle(col)
      return {
        found: true,
        displayBefore: cs.display,
        colAbsolute: cs.position === 'absolute',
        hasSvg: !!btn.querySelector('svg'),
        text: btn.textContent.trim(),
        title: btn.getAttribute('title'),
        refreshHasSvg: !!refresh.querySelector('svg'),
        refreshTitle: refresh.getAttribute('title'),
      }
    })
    console.log('mount remove (before hover):', JSON.stringify(mountRemove))
    if (!mountRemove.found) failures.push('找不到网盘卡片的悬停操作列（.mount-hover-actions，内含 .mount-remove + .mount-refresh）')
    if (mountRemove.found && mountRemove.displayBefore !== 'none') failures.push(`网盘悬停操作列平时应隐藏，实测 display=${mountRemove.displayBefore}`)
    if (mountRemove.found && !mountRemove.colAbsolute) failures.push('网盘悬停操作列应为绝对定位（不占布局）')
    if (mountRemove.found && !mountRemove.hasSvg) failures.push('网盘删除按钮应是内联 SVG 图标，不是 emoji/文字')
    if (mountRemove.found && mountRemove.text !== '') failures.push(`网盘删除按钮里不应有文字/emoji，实测 ${JSON.stringify(mountRemove.text)}`)
    if (mountRemove.found && mountRemove.title !== '移除网盘') failures.push(`网盘删除按钮 title 应为“移除网盘”，实测 ${JSON.stringify(mountRemove.title)}`)
    if (mountRemove.found && !mountRemove.refreshHasSvg) failures.push('网盘刷新容量按钮应是内联 SVG 图标，不是 emoji/文字')
    // v0.15.0 改名：它重拉的是全部网盘的容量，旧文案“刷新容量”
    // 会让人以为只刷这一张卡。
    if (mountRemove.found && mountRemove.refreshTitle !== '刷新全部容量') failures.push(`网盘刷新容量按钮 title 应为“刷新全部容量”，实测 ${JSON.stringify(mountRemove.refreshTitle)}`)
    await page.hover('.mount-card').catch(() => failures.push('悬停网盘卡片失败'))
    await page.waitForTimeout(300)
    const mountRemoveHover = await page.evaluate(() => {
      const col = document.querySelector('.mount-card .mount-hover-actions')
      if (!col) return { found: false }
      const cs = getComputedStyle(col)
      return { found: true, displayAfter: cs.display }
    })
    console.log('mount remove (on hover):', JSON.stringify(mountRemoveHover))
    if (mountRemoveHover.found && mountRemoveHover.displayAfter === 'none') failures.push('悬停网盘卡片后悬停操作列没有出现')
    const sidebar3 = await page.$('.sidebar')
    if (sidebar3) await sidebar3.screenshot({ path: path.join(SHOTS, '02e-sidebar-mount-hover.png') })
  }

  // 2f. 容量条（VISUAL_MOUNTS）：每个网盘卡片下方有容量信息，
  // 与原型 Demo v8 一致的 used/total + 百分比条；S3/R2 显示
  // “不限量”（没有配额接口，不编数字）；查询失败的那块显示
  // “容量暂不可用”，其他网盘照常显示。
  if (process.env.VISUAL_MOUNTS) {
    const quotaCheck = await page.evaluate(() => {
      const cards = [...document.querySelectorAll('.mount-card')]
      const textOf = (name) => {
        const card = cards.find((c) => c.querySelector('.mount-name')?.textContent?.includes(name))
        return card?.textContent ?? ''
      }
      const drime = textOf('Drime')
      const s3 = textOf('我的 R2')
      const local = textOf('本地照片')
      const longName = cards.find((c) => (c.querySelector('.mount-name')?.textContent ?? '').length >= 30)
      return {
        cardCount: cards.length,
        drimeHasUsage: drime.includes('已用 291.0 GB / 512.0 GB'),
        drimeHasWorkspace: drime.includes('工作区 8762'),
        drimeHasBar: !!cards.find((c) => c.querySelector('.mount-name')?.textContent?.includes('Drime'))?.querySelector('.quota-fill'),
        s3Unlimited: s3.includes('不限量'),
        localDisk: local.includes('本机磁盘'),
        localUnverified: local.includes('未验证'),
        longNameError: (longName?.textContent ?? '').includes('容量暂不可用'),
      }
    })
    console.log('sidebar quotas:', JSON.stringify(quotaCheck))
    if (!quotaCheck.drimeHasUsage) failures.push('侧栏 Drime 卡片没有显示“已用 291.0 GB / 512.0 GB”（compact 版不带“共”字）')
    if (!quotaCheck.drimeHasWorkspace) failures.push('侧栏 Drime 卡片没有显示工作区“工作区 8762”（容量是工作区口径，数字旁必须标明是哪个工作区）')
    if (!quotaCheck.drimeHasBar) failures.push('侧栏 Drime 卡片没有容量进度条（.quota-fill）')
    if (!quotaCheck.s3Unlimited) failures.push('侧栏 S3 卡片没有显示“不限量”（R2 没有配额接口）')
    if (!quotaCheck.localDisk) failures.push('侧栏本地文件夹卡片没有注明“本机磁盘”')
    if (!quotaCheck.localUnverified) failures.push('侧栏未验证的网盘（本地照片桩）没有显示“未验证”徽章')
    if (!quotaCheck.longNameError) failures.push('容量查询失败的网盘没有显示“容量暂不可用”')
    const sidebar4 = await page.$('.sidebar')
    if (sidebar4) await sidebar4.screenshot({ path: path.join(SHOTS, '02f-sidebar-quota.png') })
  }

  // 2g. 超长网盘名（VISUAL_MOUNTS，30 个汉字）：悬停删除按钮时
  // 名字仍是单行省略、不换行；删除按钮是绝对定位（不占布局），
  // 出现/消失不改变名字宽度。
  if (process.env.VISUAL_MOUNTS) {
    const longCard = await page.evaluate(() => {
      const cards = [...document.querySelectorAll('.mount-card')]
      return cards.findIndex((c) => (c.querySelector('.mount-name')?.textContent ?? '').length >= 30)
    })
    if (longCard === -1) {
      failures.push('找不到超长网盘名的卡片（30 个汉字桩）')
    } else {
      await page.locator('.mount-card').nth(longCard).hover()
      await page.waitForTimeout(300)
      const longCheck = await page.evaluate((idx) => {
        const card = [...document.querySelectorAll('.mount-card')][idx]
        if (!card) return { found: false }
        const name = card.querySelector('.mount-name')
        const col = card.querySelector('.mount-hover-actions')
        if (!name || !col) return { found: false }
        const nr = name.getBoundingClientRect()
        const cs = getComputedStyle(name)
        const ccs = getComputedStyle(col)
        return {
          found: true,
          singleLine: nr.height <= 22,
          ellipsis: cs.textOverflow === 'ellipsis' && cs.whiteSpace === 'nowrap' && cs.overflow === 'hidden',
          clipped: name.scrollWidth > name.clientWidth,
          removeVisible: ccs.display !== 'none',
          removeAbsolute: ccs.position === 'absolute',
          title: name.getAttribute('title'),
          nameLen: name.textContent?.length ?? 0,
        }
      }, longCard)
      console.log('long mount name:', JSON.stringify(longCheck))
      if (!longCheck.found) failures.push('找不到超长网盘名的名字/删除按钮元素')
      if (longCheck.found && !longCheck.singleLine) failures.push('超长网盘名悬停删除按钮时换行了（应单行省略）')
      if (longCheck.found && !longCheck.ellipsis) failures.push('超长网盘名没有省略号样式（nowrap + hidden + ellipsis）')
      if (longCheck.found && !longCheck.clipped) failures.push('超长网盘名没有被截断（应超出卡片宽度并省略）')
      if (longCheck.found && !longCheck.removeVisible) failures.push('悬停超长网盘名卡片后删除按钮没有出现')
      if (longCheck.found && !longCheck.removeAbsolute) failures.push('网盘悬停操作列应为绝对定位（不占布局），实测不是 absolute')
      if (longCheck.found && !longCheck.title) failures.push('超长网盘名没有 title 属性（悬停应显示全名）')
      const sidebar5 = await page.$('.sidebar')
      if (sidebar5) await sidebar5.screenshot({ path: path.join(SHOTS, '02g-sidebar-long-name.png') })
    }
  }

  // 2d. 上传目标文件夹选择（问题 2，VISUAL_MOUNTS）：模拟一次拖放，
  // 必须先弹目标选择框（默认当前文件夹、显示完整路径），而不是
  // 立即上传；框里有新建文件夹入口。
  if (process.env.VISUAL_MOUNTS) {
    await page.evaluate(() => window.__fireFileDrop(['/tmp/照片/a.jpg', '/tmp/照片/b.jpg'], 700, 400))
    await page.waitForTimeout(600)
    const uploadDialog = await page.evaluate(() => {
      const d = [...document.querySelectorAll('.dialog')].find((el) => el.textContent?.includes('上传到哪里'))
      if (!d) return { found: false }
      return {
        found: true,
        text: d.textContent?.slice(0, 300),
        hasConfirm: [...d.querySelectorAll('button')].some((b) => b.textContent.trim() === '上传到这里'),
        hasMkdir: d.textContent?.includes('新建文件夹') ?? false,
      }
    })
    console.log('upload target dialog:', JSON.stringify(uploadDialog))
    if (!uploadDialog.found) failures.push('拖放文件后没有弹出上传目标选择框')
    if (uploadDialog.found && !uploadDialog.text?.includes('目标：我的 R2')) failures.push('上传目标框没有显示完整路径“目标：我的 R2”')
    if (uploadDialog.found && !uploadDialog.hasConfirm) failures.push('上传目标框没有“上传到这里”按钮')
    if (uploadDialog.found && !uploadDialog.hasMkdir) failures.push('上传目标框没有“新建文件夹”入口')
    await page.screenshot({ path: path.join(SHOTS, '02d-upload-target.png') })
    if (uploadDialog.found) {
      await page.locator('.dialog-actions >> text=取消').click()
      await page.waitForTimeout(400)
    }
  }

  // 2e. 批量操作栏（V0.9.0，VISUAL_MOUNTS）：勾选一行后出现
  // “已选 N 个” + 下载 / 复制到…；下载进队列有提示，复制弹
  // 出目标选择框（可切换目标网盘、确认按钮是“复制到这里”）。
  if (process.env.VISUAL_MOUNTS) {
    await page.locator('.file-table tbody input[type="checkbox"]').first().click()
    await page.waitForTimeout(400)
    const batchBar = await page.evaluate(() => {
      const body = document.body.textContent ?? ''
      return {
        hasCount: body.includes('已选 1 个'),
        hasDownload: [...document.querySelectorAll('button')].some((b) => b.textContent?.trim() === '下载到默认文件夹'),
        hasCopy: [...document.querySelectorAll('button')].some((b) => b.textContent?.trim() === '复制到…'),
        hasOldNote: body.includes('尚未接入'),
      }
    })
    console.log('batch bar:', JSON.stringify(batchBar))
    if (!batchBar.hasCount) failures.push('勾选一行后没有出现“已选 1 个”')
    if (!batchBar.hasDownload) failures.push('批量栏里没有“下载到默认文件夹”按钮')
    if (!batchBar.hasCopy) failures.push('批量栏里没有“复制到…”按钮')
    if (batchBar.hasOldNote) failures.push('批量栏里还留着“尚未接入”的旧文案')
    if (batchBar.hasDownload) {
      await page.getByRole('button', { name: '下载到默认文件夹', exact: true }).click()
      await page.waitForTimeout(600)
      const dlMsg = await page.evaluate(() => document.body.textContent ?? '')
      if (!dlMsg.includes('已把 1 个文件加入下载队列')) failures.push(`点下载后没有出现入队提示，实测 ${JSON.stringify(dlMsg.slice(0, 200))}`)
    }
    // 重新勾选（下载后选择被清空），再测复制目标框。
    await page.locator('.file-table tbody input[type="checkbox"]').first().click()
    await page.waitForTimeout(300)
    await page.getByRole('button', { name: '复制到…', exact: true }).click()
    await page.waitForTimeout(600)
    const copyDialog = await page.evaluate(() => {
      const d = [...document.querySelectorAll('.dialog')].find((el) => el.textContent?.includes('复制到哪里'))
      if (!d) return { found: false }
      return {
        found: true,
        hasMountSelect: !!d.querySelector('select'),
        hasConfirm: [...d.querySelectorAll('button')].some((b) => b.textContent.trim() === '复制到这里'),
      }
    })
    console.log('copy target dialog:', JSON.stringify(copyDialog))
    if (!copyDialog.found) failures.push('点“复制到…”后没有弹出复制目标选择框')
    if (copyDialog.found && !copyDialog.hasMountSelect) failures.push('复制目标框里不能切换目标网盘')
    if (copyDialog.found && !copyDialog.hasConfirm) failures.push('复制目标框没有“复制到这里”按钮')
    await page.screenshot({ path: path.join(SHOTS, '02e-batch-copy.png') })
    if (copyDialog.found) {
      await page.locator('.dialog-actions >> text=取消').click()
      await page.waitForTimeout(400)
    }
    // 取消选择，恢复干净态。
    await page.getByRole('button', { name: '取消选择', exact: true }).click().catch(() => {})
    await page.waitForTimeout(300)
  }

  // 2g. 文件预览（v0.9.1，VISUAL_MOUNTS）：点文件行出
  // Inspector，里面的“预览”按格式分档——图片内嵌 <img>
  //（桩给 1×1 PNG data URL，真能渲染出来）、文本/代码
  // <pre> 显示内容、Office 只给“用系统应用打开”+ 显示入口
  // （不假装能预览）、视频给 <video> 或诚实的降级文案
  // （桩浏览器认不出 zydrive-preview://，播不出来是预期的）。
  if (process.env.VISUAL_MOUNTS) {
    const openPreview = async (name) => {
      await page.locator('.file-table .row-name', { hasText: name }).first().click()
      await page.waitForTimeout(600)
      return await page.evaluate(() => {
        // 注意：侧栏本身也是 <aside>，必须用 Inspector 的类名
        // 定位，不能直接 querySelector('aside')。
        const aside = document.querySelector('aside.inspector-enter, aside.inspector-exit')
        if (!aside) return { found: false }
        const text = aside.textContent ?? ''
        const img = aside.querySelector('img')
        return {
          found: true,
          hasPreviewHead: text.includes('预览'),
          hasImg: !!img,
          imgLoaded: img ? img.complete && img.naturalWidth > 0 : false,
          imgWidth: img ? Math.round(img.getBoundingClientRect().width) : 0,
          hasPre: !!aside.querySelector('pre'),
          preText: aside.querySelector('pre')?.textContent ?? '',
          hasVideo: !!aside.querySelector('video'),
          hasIframe: !!aside.querySelector('iframe'),
          hasSystemOpen: [...aside.querySelectorAll('button')].some((b) => b.textContent?.trim() === '用系统应用打开'),
          hasReveal: [...aside.querySelectorAll('button')].some((b) => /中显示$/.test(b.textContent?.trim() ?? '')),
          hasStreamNote: text.includes('流式播放中'),
          hasVideoFallback: text.includes('播不出来'),
          text: text.slice(0, 300),
        }
      })
    }
    // 图片：内嵌 img，真渲染出来（桩是 240×120 绿块，应占满预览区宽度）。
    const pngPrev = await openPreview('封面.png')
    console.log('preview png:', JSON.stringify(pngPrev))
    if (!pngPrev.found || !pngPrev.hasPreviewHead) failures.push('点 封面.png 后 Inspector 里没有“预览”')
    if (!pngPrev.hasImg) failures.push('图片预览里没有 <img>')
    if (pngPrev.hasImg && !pngPrev.imgLoaded) failures.push('图片预览的 <img> 没有加载出来（桩是 240×120 PNG data URL，应能渲染）')
    if (pngPrev.hasImg && pngPrev.imgWidth < 100) failures.push(`图片预览的 <img> 渲染宽度只有 ${pngPrev.imgWidth}px（桩图 240×120，应占满预览区）`)
    await page.screenshot({ path: path.join(SHOTS, '02g-preview-png.png') })
    // 文本/代码：<pre> 显示内容。
    const goPrev = await openPreview('main.go')
    console.log('preview go:', JSON.stringify(goPrev))
    if (!goPrev.hasPre) failures.push('代码预览里没有 <pre>')
    if (goPrev.hasPre && !goPrev.preText.includes('package main')) failures.push(`代码预览的 <pre> 里没有桩文本，实测 ${JSON.stringify(goPrev.preText.slice(0, 100))}`)
    await page.screenshot({ path: path.join(SHOTS, '02g-preview-text.png') })
    // Office：不假装能预览，只给两个系统入口；点“用系统应用
    // 打开”后桩返回假缓存路径，显示“已用系统应用打开”。
    const xlsxPrev = await openPreview('预算.xlsx')
    console.log('preview xlsx:', JSON.stringify(xlsxPrev))
    if (!xlsxPrev.hasSystemOpen) failures.push('Office 预览里没有“用系统应用打开”按钮')
    if (!xlsxPrev.hasReveal) failures.push('Office 预览里没有“在 Finder / 文件管理器中显示”入口')
    if (xlsxPrev.hasImg || xlsxPrev.hasVideo || xlsxPrev.hasIframe) failures.push('Office 文件不应在应用内渲染（不应出现 img/video/iframe）')
    await page.getByRole('button', { name: '用系统应用打开', exact: true }).click()
    await page.waitForTimeout(500)
    const openedMsg = await page.evaluate(() => document.querySelector('aside.inspector-enter, aside.inspector-exit')?.textContent ?? '')
    if (!openedMsg.includes('已用系统应用打开')) failures.push(`点“用系统应用打开”后没有成功提示，实测 ${JSON.stringify(openedMsg.slice(0, 200))}`)
    await page.screenshot({ path: path.join(SHOTS, '02g-preview-xlsx.png') })
    // 视频：<video> 存在（桩浏览器播不出是预期的，降级文案也
    // 接受），两种状态都不能是空白。
    const mp4Prev = await openPreview('旅行.mp4')
    console.log('preview mp4:', JSON.stringify(mp4Prev))
    if (!mp4Prev.hasVideo && !mp4Prev.hasVideoFallback) failures.push('视频预览既没有 <video> 也没有降级文案（空白）')
    if (mp4Prev.hasVideo && !mp4Prev.hasStreamNote) failures.push('视频预览有 <video> 但没有“流式播放中”的说明')
    if (!mp4Prev.hasVideo && mp4Prev.hasStreamNote) failures.push('视频播不出来时不应再显示“流式播放中”')
    if (!mp4Prev.hasVideo && !mp4Prev.hasSystemOpen) failures.push('视频播不出来时应给出系统应用打开入口')
    await page.screenshot({ path: path.join(SHOTS, '02g-preview-video.png') })
    // PDF：<iframe> 内嵌。
    const pdfPrev = await openPreview('行程.pdf')
    console.log('preview pdf:', JSON.stringify(pdfPrev))
    if (!pdfPrev.hasIframe) failures.push('PDF 预览里没有 <iframe>')
    await page.screenshot({ path: path.join(SHOTS, '02g-preview-pdf.png') })
    // 关掉 Inspector，恢复干净态。
    await page.locator('aside.inspector-enter button[aria-label="关闭详情"]').click().catch(() => {})
    await page.waitForTimeout(300)
  }

  // 2h. 文件右键菜单（v0.10.0，VISUAL_MOUNTS）：右键文件行弹出
  // 菜单，含 复制到…/移动到…/重命名/删除；点 删除 出现二次
  // 确认框（桩网盘 DeleteToTrash=true，写明移入回收站），
  // 取消后菜单关闭；点 移动到… 打开移动目标框。
  if (process.env.VISUAL_MOUNTS) {
    const firstRow = page.locator('.file-table tbody tr').first()
    await firstRow.click({ button: 'right' })
    await page.waitForTimeout(400)
    const menuLabels = await page.evaluate(() => [...document.querySelectorAll('.context-menu button')].map((b) => b.textContent?.trim()))
    console.log('context menu:', JSON.stringify(menuLabels))
    for (const want of ['复制到…', '移动到…', '重命名', '删除']) {
      if (!menuLabels.includes(want)) failures.push(`右键菜单没有“${want}”`)
    }
    await page.screenshot({ path: path.join(SHOTS, '02h-context-menu.png') })
    // 删除确认框：数量 + 文件名 + 回收站文案。等 700ms 再截：
    // 对话框入场动画 500ms，400ms 截图会拍到动画中途的字。
    await page.getByRole('menuitem', { name: '删除' }).click()
    await page.waitForTimeout(700)
    const delDialog = await page.evaluate(() => {
      const d = document.querySelector('.dialog')
      return d ? d.textContent ?? '' : ''
    })
    console.log('delete dialog:', JSON.stringify(delDialog))
    if (!delDialog.includes('回收站')) failures.push('删除确认框没有写明“移入回收站”')
    // 回归（v0.10.0）：.dialog-actions 以前是 sticky，短对话
    // 框里会盖住这行字的下半截。断言底栏顶边在文案行底边之下。
    const overlap = await page.evaluate(() => {
      const d = document.querySelector('.dialog')
      const p = [...(d?.querySelectorAll('p') ?? [])].find((x) => x.textContent?.includes('回收站'))
      const bar = d?.querySelector('.dialog-actions')
      if (!p || !bar) return null
      return { pBottom: p.getBoundingClientRect().bottom, barTop: bar.getBoundingClientRect().top }
    })
    if (!overlap) failures.push('删除确认框找不到回收站文案行或底栏')
    else if (overlap.barTop < overlap.pBottom) failures.push(`删除确认框底栏盖住了回收站文案行（栏顶 ${overlap.barTop} < 行底 ${overlap.pBottom}）`)
    await page.screenshot({ path: path.join(SHOTS, '02h-delete-confirm.png') })
    await page.getByRole('button', { name: '取消' }).click()
    await page.waitForTimeout(400)
    if (await page.locator('.context-menu').count()) failures.push('点取消后右键菜单还开着')
    // 移动到… 打开目标选择框（复用 UploadTargetDialog）。
    await firstRow.click({ button: 'right' })
    await page.waitForTimeout(400)
    await page.getByRole('menuitem', { name: '移动到…' }).click()
    await page.waitForTimeout(600)
    const moveTitle = await page.locator('.dialog h1').first().textContent().catch(() => '')
    if (moveTitle?.trim() !== '移动到哪里？') failures.push(`点“移动到…”后目标框标题不对，实测 ${JSON.stringify(moveTitle)}`)
    await page.getByRole('button', { name: '取消' }).click().catch(() => {})
    await page.waitForTimeout(400)
  }

  // 拖拽测试共用：分步移动指针（每步都经过 pointermove，
  // 拖拽态的插入点才能跟上）；返回后指针停在 (tx, ty)。
  async function stepMove(fromX, fromY, tx, ty, steps = 10) {
    for (let i = 1; i <= steps; i++) {
      await page.mouse.move(fromX + (tx - fromX) * i / steps, fromY + (ty - fromY) * i / steps)
      await page.waitForTimeout(30)
    }
  }

  // 2i. 长按拖拽跨组排序（v0.11.0，VISUAL_MOUNTS）：按住“工作”
  // 组的第一张网盘卡片（我的 R2）450ms 进入拖拽态（跟随指针
  // 的是 body 下的克隆 .mount-card.dragging），拖到“个人”组
  // 第一张卡上方松手 → MoveMount 被调到 (m-s3, '个人', 0)，
  // 卡片随之换组并排到组首；拖拽中插入线出现且随指针移动。
  // 注意：分组按名字默认 sort（Unicode），“个人”排在最前——
  // 不能拿侧栏第一张卡片当源（它已在“个人”组）。
  if (process.env.VISUAL_MOUNTS) {
    const firstCard = page.locator('[data-group="工作"] .mount-card').first()
    // “工作”组在最后，卡片可能在侧栏可视区之外：先滚到可见，
    // 否则按住点落在视口外、拖拽起不来。
    await firstCard.scrollIntoViewIfNeeded()
    const draggedName = (await firstCard.locator('.mount-name').textContent())?.trim() ?? ''
    const cardBox = await firstCard.boundingBox()
    const targetCard = page.locator('[data-group="个人"] .mount-card').first()
    if (!cardBox) {
      failures.push('长按拖拽测试：找不到“工作”组网盘卡片')
    } else {
      let curX = cardBox.x + cardBox.width / 2
      let curY = cardBox.y + cardBox.height / 2
      await page.mouse.move(curX, curY)
      await page.mouse.down()
      await page.waitForTimeout(450)
      const dragging = await page.evaluate(() => !!document.querySelector('.mount-card.dragging'))
      if (!dragging) failures.push('按住网盘卡片 450ms 后没有进入拖拽态（.mount-card.dragging）')
      await page.screenshot({ path: path.join(SHOTS, '02i-drag-group.png') })
      // 目标（“个人”组首）在视口上方时：指针先停在顶部边缘，
      // 等自动滚动把目标组带进视野（最多 3s）。
      let targetBox = await targetCard.boundingBox()
      if (targetBox && targetBox.y < 40) {
        await page.mouse.move(curX, 24)
        curY = 24
        let ok = false
        for (let i = 0; i < 15; i++) {
          await page.waitForTimeout(200)
          targetBox = await targetCard.boundingBox()
          if (targetBox && targetBox.y >= 80) { ok = true; break }
        }
        if (!ok) failures.push('拖拽中自动滚动没把“个人”组带进视野')
      }
      if (targetBox) {
        const tx = targetBox.x + targetBox.width / 2
        // 先移到组首上方（index 0），记插入线位置；再移到
        // 第三张卡上方（index 2），插入线应随之下移约一张卡
        // 高（相邻插入点的线位置几乎重合，故不用 index 1）。
        await stepMove(curX, curY, tx, targetBox.y - 2)
        curX = tx; curY = targetBox.y - 2
        await page.waitForTimeout(150)
        const lineTop0 = await page.evaluate(() => document.querySelector('.insert-line')?.getBoundingClientRect().top ?? null)
        if (lineTop0 === null) failures.push('拖拽中插入线（.insert-line）没有出现')
        const thirdCard = page.locator('[data-group="个人"] .mount-card').nth(2)
        const thirdBox = await thirdCard.boundingBox()
        if (thirdBox) {
          await stepMove(curX, curY, tx, thirdBox.y - 2)
          curX = tx; curY = thirdBox.y - 2
          await page.waitForTimeout(150)
          const lineTop2 = await page.evaluate(() => document.querySelector('.insert-line')?.getBoundingClientRect().top ?? null)
          if (lineTop0 !== null && lineTop2 !== null && lineTop2 <= lineTop0 + 30) failures.push(`插入线没有随指针下移（index 0 处 ${lineTop0}，index 2 处 ${lineTop2}）`)
          // 移回组首再松手（重测：此时组首卡未被挤开，位置是自然位置）。
          targetBox = await targetCard.boundingBox()
          if (targetBox) {
            await stepMove(curX, curY, tx, targetBox.y - 2)
            curY = targetBox.y - 2
            await page.waitForTimeout(150)
          }
        }
      }
      await page.mouse.up()
      await page.waitForTimeout(600)
      const lastMove = await page.evaluate(() => window.__lastMoveMount ?? null)
      console.log('last move mount:', JSON.stringify(lastMove))
      if (!lastMove || lastMove[0] !== 'm-s3' || lastMove[1] !== '个人' || lastMove[2] !== 0) failures.push(`拖到“个人”组首后 MoveMount 没被调到 (m-s3, 个人, 0)，实测 ${JSON.stringify(lastMove)}`)
      const personalText = await page.evaluate(() => document.querySelector('[data-group="个人"]')?.textContent ?? '')
      if (draggedName && !personalText.includes(draggedName)) failures.push(`拖拽后“${draggedName}”没出现在“个人”分组里`)
    }
  }

  // 2j. 组内排序（v0.11.0，VISUAL_MOUNTS）：2i 之后“个人”组
  // 是 [我的 R2, Dropbox, OneDrive, Drime, 超长名]。长按最后
  // 一张（超长名）拖到组首松手 → MoveMount 被调到
  // (m-long, '个人', 0)，组内顺序真的变了；拖拽中跟随的克隆
  // 位置由 transform 驱动（指针移动后 transform 连续变化）。
  if (process.env.VISUAL_MOUNTS) {
    const cards = page.locator('[data-group="个人"] .mount-card')
    const lastCard = cards.last()
    await lastCard.scrollIntoViewIfNeeded()
    const lastBox = await lastCard.boundingBox()
    if (!lastBox) {
      failures.push('组内排序测试：找不到“个人”组最后一张卡片')
    } else {
      let curX = lastBox.x + lastBox.width / 2
      let curY = lastBox.y + lastBox.height / 2
      await page.mouse.move(curX, curY)
      await page.mouse.down()
      await page.waitForTimeout(450)
      const dragging = await page.evaluate(() => !!document.querySelector('.mount-card.dragging'))
      if (!dragging) failures.push('组内排序：按住卡片 450ms 后没有进入拖拽态')
      // transform 跟手：指针移到两个不同 Y，克隆的 transform
      // 应连续变化（rAF + 直写 style，不走 React 重渲染）。
      const readTransform = () => page.evaluate(() => document.querySelector('.mount-card.dragging')?.style.transform ?? '')
      await stepMove(curX, curY, curX, curY - 60)
      curY -= 60
      await page.waitForTimeout(120)
      const t1 = await readTransform()
      await stepMove(curX, curY, curX, curY - 60)
      curY -= 60
      await page.waitForTimeout(120)
      const t2 = await readTransform()
      if (!t1 || !t2 || t1 === t2) failures.push(`拖拽中卡片 transform 没有随指针连续变化（${t1} → ${t2}）`)
      // 移到组首上方松手（重测：此时组首卡未被挤开）。
      const headBox = await cards.first().boundingBox()
      if (headBox) {
        await stepMove(curX, curY, headBox.x + headBox.width / 2, headBox.y - 2)
        await page.waitForTimeout(150)
      }
      await page.mouse.up()
      await page.waitForTimeout(600)
      const lastMove = await page.evaluate(() => window.__lastMoveMount ?? null)
      console.log('last move mount (2j):', JSON.stringify(lastMove))
      if (!lastMove || lastMove[0] !== 'm-long' || lastMove[1] !== '个人' || lastMove[2] !== 0) failures.push(`组内拖到首位后 MoveMount 没被调到 (m-long, 个人, 0)，实测 ${JSON.stringify(lastMove)}`)
      const firstName = await page.evaluate(() => document.querySelector('[data-group="个人"] .mount-card .mount-name')?.textContent?.trim() ?? '')
      if (!firstName.startsWith('这是一个特别特别长的网盘名字')) failures.push(`组内排序后“个人”组首不是超长名卡片，实测“${firstName}”`)
    }
  }

  // 2k. 跨组拖拽时分组标题跟随（v0.12.0，VISUAL_MOUNTS）：2j
  // 之后“个人”组是 [超长名, 我的 R2, Dropbox, OneDrive,
  // Drime]。长按组首（超长名）拖到“存储 / 备份”组首——拖拽
  // 中“存储 / 备份”的标题行必须跟着上移一张卡高 + 间距（被
  // 拖卡片腾出的位置由它下面的内容顶上来），而“工作”组的
  // 标题在插入点之后、不应动；松手后 MoveMount 被调到
  // (m-long, '存储 / 备份', 0)。标题位移由 transform 驱动，
  // 与卡片同一个 200ms 过渡，所以读数前等 350ms 让过渡落定。
  if (process.env.VISUAL_MOUNTS) {
    await page.evaluate(() => { document.querySelector('.sidebar').scrollTop = 0 })
    await page.waitForTimeout(200)
    const srcCard = page.locator('[data-group="个人"] .mount-card').first()
    const srcBox = await srcCard.boundingBox()
    const targetCard2k = page.locator('[data-group="存储 / 备份"] .mount-card').first()
    const targetBox2k = await targetCard2k.boundingBox()
    const headerTop = (label) => page.evaluate((l) => {
      const el = [...document.querySelectorAll('[data-group-header]')].find((h) => h.textContent?.includes(l))
      return el ? el.getBoundingClientRect().top : null
    }, label)
    if (!srcBox || !targetBox2k) {
      failures.push('分组标题跟随测试：找不到源卡片或目标组首卡片')
    } else {
      const draggedName2k = (await srcCard.locator('.mount-name').textContent())?.trim() ?? ''
      const cardH = srcBox.height + 6
      const storageTopBefore = await headerTop('存储')
      const workTopBefore = await headerTop('工作')
      let curX = srcBox.x + srcBox.width / 2
      let curY = srcBox.y + srcBox.height / 2
      await page.mouse.move(curX, curY)
      await page.mouse.down()
      await page.waitForTimeout(450)
      const dragging2k = await page.evaluate(() => !!document.querySelector('.mount-card.dragging'))
      if (!dragging2k) failures.push('分组标题跟随测试：按住卡片 450ms 后没有进入拖拽态')
      await stepMove(curX, curY, targetBox2k.x + targetBox2k.width / 2, targetBox2k.y - 2)
      await page.waitForTimeout(350)
      const storageTopDuring = await headerTop('存储')
      const workTopDuring = await headerTop('工作')
      console.log('header follow:', JSON.stringify({ cardH, storageTopBefore, storageTopDuring, workTopBefore, workTopDuring }))
      if (storageTopBefore === null || storageTopDuring === null) failures.push('分组标题跟随测试：找不到“存储 / 备份”标题行')
      else if (Math.abs((storageTopDuring - storageTopBefore) + cardH) > 4) failures.push(`跨组拖拽中“存储 / 备份”标题应上移约 ${cardH.toFixed(1)}px（一张卡高 + 间距），实测移动 ${(storageTopDuring - storageTopBefore).toFixed(1)}px`)
      if (workTopBefore === null || workTopDuring === null) failures.push('分组标题跟随测试：找不到“工作”标题行')
      else if (Math.abs(workTopDuring - workTopBefore) > 4) failures.push(`跨组拖拽中“工作”标题不应动（在插入点之后），实测移动 ${(workTopDuring - workTopBefore).toFixed(1)}px`)
      await page.screenshot({ path: path.join(SHOTS, '02k-drag-header.png') })
      await page.mouse.up()
      await page.waitForTimeout(600)
      const lastMove2k = await page.evaluate(() => window.__lastMoveMount ?? null)
      console.log('last move mount (2k):', JSON.stringify(lastMove2k))
      if (!lastMove2k || lastMove2k[0] !== 'm-long' || lastMove2k[1] !== '存储 / 备份' || lastMove2k[2] !== 0) failures.push(`拖到“存储 / 备份”组首后 MoveMount 没被调到 (m-long, 存储 / 备份, 0)，实测 ${JSON.stringify(lastMove2k)}`)
      const storageText = await page.evaluate(() => document.querySelector('[data-group="存储 / 备份"]')?.textContent ?? '')
      if (draggedName2k && !storageText.includes(draggedName2k)) failures.push(`拖拽后“${draggedName2k}”没出现在“存储 / 备份”分组里`)
    }
  }

  // 2l. 命令面板居中（v0.13.0）：点顶栏搜索框弹出命令面板，面板
  // 打开第一帧就应该已经在水平正中——v0.13.0 之前入场动画
  // rise-sm 的 transform 会在 500ms 动画期间顶掉居中的
  // translateX(-50%)，面板先出现在右侧、动画结束才弹回中间。
  // 断言：打开瞬间与动画结束后的中心点都在视口正中（±4px），
  // 且两者之间没有横向跳动。
  {
    await page.locator('.app-header .input').click()
    await page.waitForTimeout(80)
    const paletteFirst = await page.evaluate(() => {
      const p = document.querySelector('.palette')
      if (!p) return { found: false }
      const r = p.getBoundingClientRect()
      return { found: true, centerX: Math.round(r.left + r.width / 2), viewportW: window.innerWidth }
    })
    console.log('palette first paint:', JSON.stringify(paletteFirst))
    await page.waitForTimeout(700)
    const paletteSettled = await page.evaluate(() => {
      const p = document.querySelector('.palette')
      if (!p) return { found: false }
      const r = p.getBoundingClientRect()
      return { found: true, centerX: Math.round(r.left + r.width / 2), viewportW: window.innerWidth }
    })
    console.log('palette settled:', JSON.stringify(paletteSettled))
    if (!paletteFirst.found) failures.push('点顶栏搜索框后命令面板没有打开（.palette）')
    if (!paletteSettled.found) failures.push('命令面板打开后消失了（.palette）')
    for (const [when, m] of [['打开瞬间', paletteFirst], ['动画结束', paletteSettled]]) {
      if (m.found && Math.abs(m.centerX - m.viewportW / 2) > 4) {
        failures.push(`命令面板${when}不在水平正中：中心 ${m.centerX}，视口中点 ${Math.round(m.viewportW / 2)}`)
      }
    }
    if (paletteFirst.found && paletteSettled.found && Math.abs(paletteFirst.centerX - paletteSettled.centerX) > 4) {
      failures.push(`命令面板打开时横向跳动：首帧中心 ${paletteFirst.centerX} → 落位 ${paletteSettled.centerX}`)
    }
    await page.screenshot({ path: path.join(SHOTS, '02l-palette.png') })
    await page.keyboard.press('Escape')
    await page.waitForTimeout(400)
  }

  // 2m. 列表/网格视图切换（v0.13.0，VISUAL_MOUNTS）：文件工具栏
  // 右上角（刷新旁）有一组分段按钮（列表/网格）；切网格后是
  // 卡片视图、表格消失；刷新页面后仍是网格（localStorage 持久
  // 化）；切回列表后表格回来。
  if (process.env.VISUAL_MOUNTS) {
    const toggle = await page.evaluate(() => {
      const t = document.querySelector('.view-toggle')
      if (!t) return { found: false }
      return {
        found: true,
        buttons: [...t.querySelectorAll('button')].map((b) => b.getAttribute('aria-label')),
        listActive: t.querySelector('button[aria-label="列表视图"]')?.classList.contains('active') ?? false,
        hasTable: !!document.querySelector('.file-table'),
        hasGrid: !!document.querySelector('.file-grid'),
      }
    })
    console.log('view toggle:', JSON.stringify(toggle))
    if (!toggle.found) failures.push('文件工具栏没有视图切换分段按钮（.view-toggle）')
    if (toggle.found && (!toggle.buttons?.includes('列表视图') || !toggle.buttons?.includes('网格视图'))) failures.push(`视图切换应有“列表视图/网格视图”两个按钮，实测 ${JSON.stringify(toggle.buttons)}`)
    if (toggle.found && !toggle.listActive) failures.push('默认视图应是列表（列表按钮激活）')
    if (toggle.found && !toggle.hasTable) failures.push('默认应显示文件表格（.file-table）')
    if (toggle.found && toggle.hasGrid) failures.push('默认不应显示网格视图（.file-grid）')
    await page.getByRole('button', { name: '网格视图', exact: true }).click()
    await page.waitForTimeout(400)
    const gridOn = await page.evaluate(() => ({
      hasTable: !!document.querySelector('.file-table'),
      hasGrid: !!document.querySelector('.file-grid'),
      cards: document.querySelectorAll('.file-grid-card').length,
      gridActive: document.querySelector('button[aria-label="网格视图"]')?.classList.contains('active') ?? false,
    }))
    console.log('view grid:', JSON.stringify(gridOn))
    if (!gridOn.hasGrid || gridOn.cards === 0) failures.push('点网格视图后没有出现文件卡片（.file-grid-card）')
    if (gridOn.hasTable) failures.push('切到网格视图后文件表格应消失')
    if (!gridOn.gridActive) failures.push('切到网格视图后网格按钮应激活')
    await page.screenshot({ path: path.join(SHOTS, '02m-view-grid.png') })
    // 持久化：刷新页面后仍是网格。
    await page.reload({ waitUntil: 'networkidle' })
    await page.waitForTimeout(1000)
    const gridPersisted = await page.evaluate(() => ({
      hasGrid: !!document.querySelector('.file-grid'),
      cards: document.querySelectorAll('.file-grid-card').length,
    }))
    console.log('view grid persisted:', JSON.stringify(gridPersisted))
    if (!gridPersisted.hasGrid || gridPersisted.cards === 0) failures.push('刷新页面后网格视图没有保持（应从 localStorage 恢复）')
    await page.getByRole('button', { name: '列表视图', exact: true }).click()
    await page.waitForTimeout(400)
    const listBack = await page.evaluate(() => ({
      hasTable: !!document.querySelector('.file-table'),
      hasGrid: !!document.querySelector('.file-grid'),
    }))
    if (!listBack.hasTable) failures.push('切回列表视图后文件表格没有回来')
    if (listBack.hasGrid) failures.push('切回列表视图后网格视图应消失')
  }

  // 2n. 右上角 Toast（v0.13.0，VISUAL_MOUNTS）：右键菜单里点
  // “在文件管理器中显示”后，确认消息以右上角 Toast 出现（不
  // 再是把文件列表往下顶的 inline 提示条）；5s 自动消失；悬
  // 停暂停计时、移走继续；X 立即关闭。
  if (process.env.VISUAL_MOUNTS) {
    // “在文件管理器中显示”只对文件出现（文件夹没有）——右键
    // 第一行文件，而不是第一行（桩数据第一行是文件夹）。
    const fileRowIdx = await page.evaluate(() => {
      const rows = [...document.querySelectorAll('.file-table tbody tr')]
      return rows.findIndex((r) => (r.children[2]?.textContent?.trim() ?? '') !== '—')
    })
    if (fileRowIdx === -1) {
      failures.push('找不到文件行（右键菜单的“在文件管理器中显示”只对文件出现）')
    } else {
      const firstRow2n = page.locator('.file-table tbody tr').nth(fileRowIdx)
      await firstRow2n.click({ button: 'right' })
      await page.waitForTimeout(400)
      await page.getByRole('menuitem', { name: '在文件管理器中显示' }).click()
      await page.waitForTimeout(400)
    const toast = await page.evaluate(() => {
      const host = document.querySelector('.toast-host')
      const t = document.querySelector('.toast')
      if (!host || !t) return { found: false }
      const r = t.getBoundingClientRect()
      return {
        found: true,
        text: t.textContent ?? '',
        right: Math.round(r.right),
        top: Math.round(r.top),
        viewportW: window.innerWidth,
        hasClose: !!t.querySelector('.toast-close'),
      }
    })
    console.log('toast:', JSON.stringify(toast))
    if (!toast.found) failures.push('点“在文件管理器中显示”后没有出现右上角 Toast（.toast-host .toast）')
    if (toast.found && !toast.text.includes('已在文件管理器中打开')) failures.push(`Toast 文案不对：${JSON.stringify(toast.text)}`)
    if (toast.found && Math.abs(toast.right - toast.viewportW) > 32) failures.push(`Toast 应贴右上角，实测右缘 ${toast.right}，视口宽 ${toast.viewportW}`)
    if (toast.found && toast.top > 100) failures.push(`Toast 应在顶部附近，实测 top=${toast.top}`)
    if (toast.found && !toast.hasClose) failures.push('Toast 没有 X 关闭按钮（.toast-close）')
    await page.screenshot({ path: path.join(SHOTS, '02n-toast.png') })
    // 悬停暂停：等 1s 后悬停，再等 4.5s（已超过 5s 总时长），
    // Toast 仍应在；移走后剩余时间走完，Toast 消失。
    await page.waitForTimeout(1000)
    await page.hover('.toast')
    await page.waitForTimeout(4500)
    const toastPaused = await page.evaluate(() => !!document.querySelector('.toast'))
    if (!toastPaused) failures.push('悬停 Toast 时它消失了（悬停应暂停 5s 倒计时）')
    await page.mouse.move(0, 0)
    await page.waitForTimeout(4500)
    const toastGone = await page.evaluate(() => !document.querySelector('.toast'))
    if (!toastGone) failures.push('移开鼠标后 Toast 没有自动消失（应走完剩余时间后关闭）')
    }
  }

  // 2o–2t. 全屏预览 overlay（v0.14.0）：双击文件打开、Esc 关
  // 闭、箭头/方向键切换、图片缩放、PDF.js 渲染、代码高亮 +
  // 行号、Markdown 渲染。桩列表里按名找行（双击会先触发两
  // 次单击选中行，无害）；overlay 打开后焦点在 overlay 根节
  // 点上，所以 Esc / 方向键直接发给页面即可。
  const rowIdxByName = (name) =>
    page.evaluate((n) => {
      const rows = [...document.querySelectorAll('.file-table tbody tr')]
      return rows.findIndex((r) => r.textContent?.includes(n))
    }, name)
  const openOverlayByName = async (name) => {
    const idx = await rowIdxByName(name)
    if (idx === -1) {
      failures.push(`找不到“${name}”行（overlay 断言）`)
      return false
    }
    await page.locator('.file-table tbody tr').nth(idx).dblclick()
    await page.waitForTimeout(800)
    return true
  }
  const closeOverlayByEsc = async () => {
    await page.keyboard.press('Escape')
    await page.waitForTimeout(500)
    if (await page.evaluate(() => !!document.querySelector('.preview-overlay'))) {
      failures.push('按 Esc 后 overlay 没有关闭')
    }
  }

  // 2o. 双击图片打开 overlay：盖满窗口、深色遮罩、标题是文
  // 件名、图片真渲染出来（桩是 240×120 品牌绿 PNG data URL）。
  if (process.env.VISUAL_MOUNTS) {
    if (await openOverlayByName('封面.png')) {
      const ov = await page.evaluate(() => {
        const el = document.querySelector('.preview-overlay')
        if (!el) return { found: false }
        const r = el.getBoundingClientRect()
        const cs = getComputedStyle(el)
        const img = el.querySelector('.preview-image-wrap img')
        return {
          found: true,
          w: Math.round(r.width),
          h: Math.round(r.height),
          vw: window.innerWidth,
          vh: window.innerHeight,
          bg: cs.backgroundColor,
          title: el.querySelector('.preview-title')?.textContent ?? '',
          imgNatural: img ? img.naturalWidth : -1,
          hasTopbar: !!el.querySelector('.preview-topbar'),
          hasToolbar: !!el.querySelector('.preview-toolbar'),
          hasInfoBtn: !!el.querySelector('button[aria-label="文件信息"]'),
        }
      })
      console.log('overlay open:', JSON.stringify(ov))
      if (!ov.found) failures.push('双击图片后全屏预览没有打开（.preview-overlay）')
      if (ov.found && (ov.w !== ov.vw || ov.h !== ov.vh)) failures.push(`overlay 没有盖满窗口：${ov.w}×${ov.h}，视口 ${ov.vw}×${ov.vh}`)
      if (ov.found && ov.bg !== 'rgba(17, 17, 17, 0.92)') failures.push(`overlay 遮罩应是 #111111 92%（rgba(17,17,17,0.92)），实测 ${ov.bg}`)
      if (ov.found && ov.title !== '封面.png') failures.push(`overlay 标题应是“封面.png”，实测 ${JSON.stringify(ov.title)}`)
      if (ov.found && ov.imgNatural !== 240) failures.push(`overlay 里图片没有真渲染出来（naturalWidth 应是 240），实测 ${ov.imgNatural}`)
      if (ov.found && (!ov.hasTopbar || !ov.hasToolbar || !ov.hasInfoBtn)) failures.push('overlay 缺顶栏 / 底部工具条 / 信息按钮')
      await page.screenshot({ path: path.join(SHOTS, '02o-overlay.png') })
      // 2p. Esc 关闭（和 2o 同一次打开里测，省一次双击）。
      await closeOverlayByEsc()
    }
  }

  // 2q. 箭头按钮 / 方向键切换文件：按当前目录顺序走，不包
  // 含文件夹；切到视频（桩是无效字节）只认标题——降级面板
  // 是预期行为，不认视频元素。v0.15.0 起顺序跟列表的可见排
  // 序走（名称升序）：封面.png 的下一个是合同.docx，不再是
  // 桩列表原顺序里的旅行.mp4。
  if (process.env.VISUAL_MOUNTS) {
    if (await openOverlayByName('封面.png')) {
      await page.getByRole('button', { name: '下一个文件' }).click()
      await page.waitForTimeout(600)
      const titleNext = await page.evaluate(() => document.querySelector('.preview-title')?.textContent ?? '')
      if (titleNext !== '合同.docx') failures.push(`点“下一个文件”后标题应是“合同.docx”（名称升序里的下一项），实测 ${JSON.stringify(titleNext)}`)
      await page.keyboard.press('ArrowLeft')
      await page.waitForTimeout(600)
      const titlePrev = await page.evaluate(() => document.querySelector('.preview-title')?.textContent ?? '')
      if (titlePrev !== '封面.png') failures.push(`按 ← 后标题应回到“封面.png”，实测 ${JSON.stringify(titlePrev)}`)
      await closeOverlayByEsc()
    }
  }

  // 2r. 图片缩放：工具条“放大”后缩放标签从“适应”变
  // “125%”（桩图 240×120，比容器小，fit 系数是 1），图
  // 片实际宽度也跟着变大。
  if (process.env.VISUAL_MOUNTS) {
    if (await openOverlayByName('封面.png')) {
      const before = await page.evaluate(() => ({
        label: document.querySelector('.preview-toolbar .preview-tb-label')?.textContent ?? '',
        imgW: document.querySelector('.preview-image-wrap img')?.clientWidth ?? -1,
      }))
      await page.getByRole('button', { name: '放大' }).click()
      await page.waitForTimeout(500)
      const after = await page.evaluate(() => ({
        label: document.querySelector('.preview-toolbar .preview-tb-label')?.textContent ?? '',
        imgW: document.querySelector('.preview-image-wrap img')?.clientWidth ?? -1,
      }))
      console.log('image zoom:', JSON.stringify({ before, after }))
      if (before.label !== '适应') failures.push(`图片初始缩放标签应是“适应”，实测 ${JSON.stringify(before.label)}`)
      if (after.label !== '125%') failures.push(`点放大后缩放标签应是“125%”，实测 ${JSON.stringify(after.label)}`)
      if (!(after.imgW > before.imgW)) failures.push(`点放大后图片应变大：${before.imgW} → ${after.imgW}`)
      await page.screenshot({ path: path.join(SHOTS, '02r-zoom.png') })
      await closeOverlayByEsc()
    }
  }

  // 2s. PDF.js 渲染：双击 PDF 后 overlay 里是 canvas（不是
  // iframe），canvas 真有尺寸；页码显示 1 / 1。
  if (process.env.VISUAL_MOUNTS) {
    if (await openOverlayByName('行程.pdf')) {
      let rendered = false
      try {
        await page.waitForFunction(
          () => {
            const c = document.querySelector('.preview-pdf canvas')
            return c && c.width > 0 && c.height > 0
          },
          { timeout: 8000 },
        )
        rendered = true
      } catch {
        rendered = false
      }
      const pdf = await page.evaluate(() => {
        const el = document.querySelector('.preview-overlay')
        const canvas = el?.querySelector('.preview-pdf canvas')
        return {
          hasCanvas: !!canvas,
          canvasW: canvas ? canvas.width : -1,
          canvasH: canvas ? canvas.height : -1,
          hasIframe: !!el?.querySelector('iframe'),
          pageInput: el?.querySelector('.preview-page-input')?.value ?? '',
          pageLabels: [...(el?.querySelectorAll('.preview-tb-label') ?? [])].map((e) => e.textContent),
        }
      })
      console.log('pdf overlay:', JSON.stringify(pdf))
      if (!pdf.hasCanvas) failures.push('PDF overlay 里没有 canvas（应是 PDF.js 渲染，不是 iframe）')
      if (pdf.hasIframe) failures.push('PDF overlay 里还有 iframe（v0.14.0 应换成 PDF.js）')
      if (!rendered || !(pdf.canvasW > 0 && pdf.canvasH > 0)) failures.push(`PDF canvas 没有渲染出页面：${pdf.canvasW}×${pdf.canvasH}`)
      if (pdf.pageInput !== '1') failures.push(`PDF 页码输入应是 1，实测 ${JSON.stringify(pdf.pageInput)}`)
      if (!pdf.pageLabels.includes('/ 1')) failures.push(`PDF 应显示总页数“/ 1”，实测 ${JSON.stringify(pdf.pageLabels)}`)
      await page.screenshot({ path: path.join(SHOTS, '02s-pdf.png') })
      await closeOverlayByEsc()
    }
  }

  // 2t. 代码高亮 + 行号、Markdown 渲染：Go 文件里
  // `package` 应是 .hljs-keyword，行号 5 行（桩文本 4 行 +
  // 末尾空行）；Markdown 桩渲染出 h1“说明”与 2 项列表。
  if (process.env.VISUAL_MOUNTS) {
    if (await openOverlayByName('main.go')) {
      let highlighted = false
      try {
        await page.waitForFunction(() => !!document.querySelector('.preview-code-body .hljs-keyword'), { timeout: 5000 })
        highlighted = true
      } catch {
        highlighted = false
      }
      const code = await page.evaluate(() => {
        const el = document.querySelector('.preview-overlay')
        const body = el?.querySelector('.preview-code-body')
        return {
          hasKeyword: !!body?.querySelector('.hljs-keyword'),
          keywordText: body?.querySelector('.hljs-keyword')?.textContent ?? '',
          gutterLines: el?.querySelectorAll('.preview-code-gutter > div').length ?? 0,
          hasTitleOrComment: !!body?.querySelector('.hljs-title, .hljs-comment'),
        }
      })
      console.log('code overlay:', JSON.stringify(code))
      if (!highlighted || !code.hasKeyword) failures.push('代码预览没有语法高亮（.hljs-keyword）')
      if (code.keywordText !== 'package') failures.push(`高亮的关键字应是“package”，实测 ${JSON.stringify(code.keywordText)}`)
      if (code.gutterLines !== 5) failures.push(`代码行号应有 5 行，实测 ${code.gutterLines}`)
      if (!code.hasTitleOrComment) failures.push('代码预览没有高亮出函数名/注释（.hljs-title / .hljs-comment）')
      await page.screenshot({ path: path.join(SHOTS, '02t-code.png') })
      await closeOverlayByEsc()
    }
    if (await openOverlayByName('说明.md')) {
      let mdRendered = false
      try {
        await page.waitForFunction(() => !!document.querySelector('.preview-md h1'), { timeout: 5000 })
        mdRendered = true
      } catch {
        mdRendered = false
      }
      const md = await page.evaluate(() => {
        const el = document.querySelector('.preview-overlay')
        const m = el?.querySelector('.preview-md')
        return {
          h1: m?.querySelector('h1')?.textContent ?? '',
          items: [...(m?.querySelectorAll('ul li') ?? [])].map((li) => li.textContent),
        }
      })
      console.log('markdown overlay:', JSON.stringify(md))
      if (!mdRendered) failures.push('Markdown 没有渲染出 h1（.preview-md h1）')
      if (md.h1 !== '说明') failures.push(`Markdown 应渲染出 h1“说明”，实测 ${JSON.stringify(md.h1)}`)
      if (md.items.length !== 2) failures.push(`Markdown 列表应有 2 项，实测 ${JSON.stringify(md.items)}`)
      await page.screenshot({ path: path.join(SHOTS, '02t-markdown.png') })
      await closeOverlayByEsc()
    }
  }

  // 2u. 新建文件夹 + 上传文件入口（v0.15.0，VISUAL_MOUNTS）：
  // 面包屑行有“新建文件夹 / 上传文件”按钮与“13 项 · 共 …”计数；
  // 新建：输名 → 确定 → 桩 Mkdir 返回 null，列表重拉（桩仍返
  // 回同一份列表）+ 出现“已新建文件夹”消息；上传：桩 PickFiles
  // 返回一条路径，弹出目标选择框（标题“上传到哪里？”）。
  if (process.env.VISUAL_MOUNTS) {
    const headerBtns = await page.evaluate(() => ({
      hasNewFolder: [...document.querySelectorAll('button')].some((b) => b.textContent?.trim() === '新建文件夹'),
      hasUpload: [...document.querySelectorAll('button')].some((b) => b.textContent?.trim() === '上传文件'),
      hasCount: /13 项/.test(document.body.textContent ?? ''),
    }))
    console.log('file header:', JSON.stringify(headerBtns))
    if (!headerBtns.hasNewFolder) failures.push('文件工具栏没有“新建文件夹”按钮')
    if (!headerBtns.hasUpload) failures.push('文件工具栏没有“上传文件”按钮')
    if (!headerBtns.hasCount) failures.push('面包屑行没有“13 项”计数')
    await page.getByRole('button', { name: '新建文件夹', exact: true }).click()
    await page.waitForTimeout(400)
    const newFolderDlg = await page.evaluate(() => {
      const d = [...document.querySelectorAll('.dialog')].find((el) => el.textContent?.includes('新建文件夹'))
      if (!d) return { found: false }
      return { found: true, hasInput: !!d.querySelector('input'), confirmDisabled: d.querySelector('.dialog-actions button:last-child')?.disabled ?? null }
    })
    console.log('new folder dialog:', JSON.stringify(newFolderDlg))
    if (!newFolderDlg.found) failures.push('点“新建文件夹”后没有弹出对话框')
    if (newFolderDlg.found && !newFolderDlg.hasInput) failures.push('新建文件夹对话框里没有名称输入框')
    if (newFolderDlg.found && newFolderDlg.confirmDisabled !== true) failures.push('新建文件夹：空名时确定按钮应禁用')
    if (newFolderDlg.found) {
      await page.locator('.dialog input').fill('新文件夹')
      await page.waitForTimeout(200)
      await page.getByRole('button', { name: '确定', exact: true }).click()
      await page.waitForTimeout(700)
      const afterMkdir = await page.evaluate(() => ({
        dialogGone: ![...document.querySelectorAll('.dialog')].some((el) => el.textContent?.includes('新建文件夹')),
        hasMsg: (document.body.textContent ?? '').includes('已新建文件夹'),
      }))
      console.log('after mkdir:', JSON.stringify(afterMkdir))
      if (!afterMkdir.dialogGone) failures.push('新建文件夹确定后对话框没有关闭')
      if (!afterMkdir.hasMsg) failures.push('新建文件夹后没有出现“已新建文件夹”消息')
      await page.screenshot({ path: path.join(SHOTS, '02u-new-folder.png') })
    }
    await page.getByRole('button', { name: '上传文件', exact: true }).click()
    await page.waitForTimeout(500)
    const uploadDlg = await page.evaluate(() => {
      const d = [...document.querySelectorAll('.dialog')].find((el) => el.textContent?.includes('上传到哪里'))
      // 目标框只显示待传文件数（不列文件名——路径可能很长）：
      // 桩 PickFiles 返回 1 条路径，应显示“1 个文件将上传”。
      return { found: !!d, showsCount: d ? (d.textContent ?? '').includes('1 个文件将上传') : false }
    })
    console.log('upload target dialog:', JSON.stringify(uploadDlg))
    if (!uploadDlg.found) failures.push('点“上传文件”后没有弹出目标选择框（桩 PickFiles 应返回一条路径）')
    if (uploadDlg.found && !uploadDlg.showsCount) failures.push('上传目标框里没有显示“1 个文件将上传”（桩只选了 1 个文件）')
    await page.screenshot({ path: path.join(SHOTS, '02u-upload-target.png') })
    if (uploadDlg.found) {
      await page.locator('.dialog-actions >> text=取消').click()
      await page.waitForTimeout(400)
    }
  }

  // 2v. 列表排序 + 全选（v0.15.0，VISUAL_MOUNTS）：默认名称升
  // 序、文件夹在前（首行是“照片”）；点“大小”升序首文件是
  // 最小的笔记.txt，再点一次降序首文件是最大的旅行.mp4；
  // 表头复选框全选 13 项、行带 .checked（已选 tint）、批量栏
  // 出现“已选 13 个”；再点一次全部取消。
  if (process.env.VISUAL_MOUNTS) {
    // 文件夹的 .row-name 里带一个“›”进入标记，比较时去掉。
    const rowNames = async () => page.evaluate(() => [...document.querySelectorAll('.file-table tbody tr .row-name')].map((el) => (el.textContent?.trim() ?? '').replace(/›$/, '')))
    const firstRows = await rowNames()
    console.log('default order:', JSON.stringify(firstRows.slice(0, 3)))
    if (firstRows[0] !== '照片') failures.push(`默认排序首行应是文件夹“照片”，实测 ${JSON.stringify(firstRows[0])}`)
    await page.getByRole('button', { name: '按大小排序', exact: true }).click()
    await page.waitForTimeout(400)
    const sizeAsc = await rowNames()
    console.log('size asc:', JSON.stringify(sizeAsc.slice(0, 3)))
    if (sizeAsc[0] !== '照片' || sizeAsc[1] !== '笔记.txt') failures.push(`按大小升序应是 照片 → 笔记.txt，实测 ${JSON.stringify(sizeAsc.slice(0, 2))}`)
    await page.getByRole('button', { name: '按大小排序', exact: true }).click()
    await page.waitForTimeout(400)
    const sizeDesc = await rowNames()
    console.log('size desc:', JSON.stringify(sizeDesc.slice(0, 3)))
    if (sizeDesc[0] !== '照片' || sizeDesc[1] !== '旅行.mp4') failures.push(`按大小降序应是 照片 → 旅行.mp4，实测 ${JSON.stringify(sizeDesc.slice(0, 2))}`)
    await page.screenshot({ path: path.join(SHOTS, '02v-sort-size.png') })
    // 回到默认名称升序（后面的断言按桩列表原顺序认行）。
    // 注意：名称是 localeCompare('zh')，中文按拼音排在拉丁
    // 字母前——首两行是 照片 → 备份.zip，不是 main.go。
    await page.getByRole('button', { name: '按名称排序', exact: true }).click()
    await page.waitForTimeout(400)
    const sortReset = await rowNames()
    if (sortReset[0] !== '照片' || sortReset[1] !== '备份.zip') failures.push(`点回名称排序后首两行应是 照片 → 备份.zip，实测 ${JSON.stringify(sortReset.slice(0, 2))}`)
    // 全选：表头复选框（thead 里唯一的 checkbox）。
    await page.locator('.file-table thead input[type="checkbox"]').click()
    await page.waitForTimeout(400)
    const checkAll = await page.evaluate(() => ({
      checkedRows: document.querySelectorAll('.file-table tbody tr.checked').length,
      checkedBoxes: document.querySelectorAll('.file-table tbody input[type="checkbox"]:checked').length,
      hasCount: (document.body.textContent ?? '').includes('已选 13 个'),
      bulkBar: !!document.querySelector('.bulkbar'),
    }))
    console.log('check all:', JSON.stringify(checkAll))
    if (checkAll.checkedRows !== 13) failures.push(`全选后应有 13 行带 .checked tint，实测 ${checkAll.checkedRows}`)
    if (checkAll.checkedBoxes !== 13) failures.push(`全选后应有 13 个行复选框被勾选，实测 ${checkAll.checkedBoxes}`)
    if (!checkAll.hasCount) failures.push('全选后批量栏没有出现“已选 13 个”')
    if (!checkAll.bulkBar) failures.push('全选后批量栏没有出现（.bulkbar）')
    await page.screenshot({ path: path.join(SHOTS, '02v-check-all.png') })
    await page.locator('.file-table thead input[type="checkbox"]').click()
    await page.waitForTimeout(400)
    const unchecked = await page.evaluate(() => document.querySelectorAll('.file-table tbody input[type="checkbox"]:checked').length)
    if (unchecked !== 0) failures.push(`再点表头复选框后应全部取消，实测仍有 ${unchecked} 个勾选`)
  }

  // 2w. Inspector 操作按钮（v0.15.0，VISUAL_MOUNTS）：点文件
  // 行后 Inspector 文件名下方有 打开 / 下载 / 复制到…（复用
  // 右键菜单的同一批 handler）；点下载出现入队提示；点复制
  // 到…弹出目标选择框。
  if (process.env.VISUAL_MOUNTS) {
    await page.locator('.file-table .row-name', { hasText: '封面.png' }).first().click()
    await page.waitForTimeout(600)
    const inspBtns = await page.evaluate(() => {
      const aside = document.querySelector('aside.inspector-enter, aside.inspector-exit')
      if (!aside) return { found: false }
      const names = [...aside.querySelectorAll('button')].map((b) => b.textContent?.trim())
      return { found: true, hasOpen: names.includes('打开'), hasDownload: names.includes('下载'), hasCopy: names.includes('复制到…'), inActionsRow: !!aside.querySelector('.insp-actions') }
    })
    console.log('inspector actions:', JSON.stringify(inspBtns))
    if (!inspBtns.found) failures.push('点 封面.png 后 Inspector 没有打开')
    if (inspBtns.found && !inspBtns.inActionsRow) failures.push('Inspector 里没有 .insp-actions 按钮行')
    if (inspBtns.found && !inspBtns.hasOpen) failures.push('Inspector 里没有“打开”按钮')
    if (inspBtns.found && !inspBtns.hasDownload) failures.push('Inspector 里没有“下载”按钮')
    if (inspBtns.found && !inspBtns.hasCopy) failures.push('Inspector 里没有“复制到…”按钮（需要存在其他网盘，桩有多网盘）')
    await page.screenshot({ path: path.join(SHOTS, '02w-inspector-actions.png') })
    if (inspBtns.found && inspBtns.hasDownload) {
      await page.locator('aside.inspector-enter .insp-actions button', { hasText: '下载' }).click()
      await page.waitForTimeout(600)
      const dlMsg = await page.evaluate(() => document.body.textContent ?? '')
      if (!dlMsg.includes('已把 1 个文件加入下载队列')) failures.push(`Inspector 点下载后没有出现入队提示，实测 ${JSON.stringify(dlMsg.slice(0, 200))}`)
    }
    if (inspBtns.found && inspBtns.hasCopy) {
      await page.locator('aside.inspector-enter .insp-actions button', { hasText: '复制到…' }).click()
      await page.waitForTimeout(600)
      const copyDlg = await page.evaluate(() => [...document.querySelectorAll('.dialog')].some((el) => el.textContent?.includes('复制到哪里')))
      if (!copyDlg) failures.push('Inspector 点“复制到…”后没有弹出目标选择框')
      if (copyDlg) {
        await page.locator('.dialog-actions >> text=取消').click()
        await page.waitForTimeout(400)
      }
    }
  }

  // 2x. 批量栏在表格上方（v0.15.0，VISUAL_MOUNTS）：勾选一行
  // 后 .bulkbar 出现在表格上方（top 小于表格 top）、淡绿底
  // （--selected #f0fdf4），且有“移动到…”并能弹出移动目标框。
  if (process.env.VISUAL_MOUNTS) {
    await page.locator('.file-table tbody input[type="checkbox"]').first().click()
    await page.waitForTimeout(400)
    // 先把栏滚回视口顶部再量位置——它是 sticky，页面滚过之
    // 后量到的 top 会是吸附位置，不是文档流里的相对位置。
    await page.evaluate(() => document.querySelector('.bulkbar')?.scrollIntoView({ block: 'start' }))
    await page.waitForTimeout(200)
    const bulkPos = await page.evaluate(() => {
      const bar = document.querySelector('.bulkbar')
      const table = document.querySelector('.file-table')
      if (!bar || !table) return { found: false }
      const b = bar.getBoundingClientRect()
      const t = table.getBoundingClientRect()
      return {
        found: true,
        barTop: Math.round(b.top),
        tableTop: Math.round(t.top),
        above: b.top < t.top,
        bg: getComputedStyle(bar).backgroundColor,
        hasMove: [...bar.querySelectorAll('button')].some((x) => x.textContent?.trim() === '移动到…'),
        hasDownload: [...bar.querySelectorAll('button')].some((x) => x.textContent?.trim() === '下载到默认文件夹'),
      }
    })
    console.log('bulk bar above table:', JSON.stringify(bulkPos))
    if (!bulkPos.found) failures.push('勾选一行后没有出现 .bulkbar（应已移到表格上方）')
    if (bulkPos.found && !bulkPos.above) failures.push(`批量栏应在表格上方，实测 bar top=${bulkPos.barTop}，table top=${bulkPos.tableTop}`)
    if (bulkPos.found && bulkPos.bg !== 'rgb(240, 253, 244)') failures.push(`批量栏应是淡绿底 --selected #f0fdf4，实测 ${bulkPos.bg}`)
    if (bulkPos.found && !bulkPos.hasMove) failures.push('批量栏里没有“移动到…”按钮')
    if (bulkPos.found && !bulkPos.hasDownload) failures.push('批量栏里丢了“下载到默认文件夹”按钮')
    await page.screenshot({ path: path.join(SHOTS, '02x-bulkbar-above.png') })
    if (bulkPos.found && bulkPos.hasMove) {
      await page.getByRole('button', { name: '移动到…', exact: true }).click()
      await page.waitForTimeout(600)
      const moveDlg = await page.evaluate(() => [...document.querySelectorAll('.dialog')].some((el) => el.textContent?.includes('移动到哪里')))
      if (!moveDlg) failures.push('批量栏点“移动到…”后没有弹出目标选择框')
      if (moveDlg) {
        await page.locator('.dialog-actions >> text=取消').click()
        await page.waitForTimeout(400)
      }
    }
    await page.getByRole('button', { name: '取消选择', exact: true }).click().catch(() => {})
    await page.waitForTimeout(300)
  }

  // 2y. 命令面板边输边搜 + 方向键（v0.15.0，VISUAL_MOUNTS）：
  // 不按回车、只输入就出结果（150ms 防抖；桩 Search 按名过滤）；
  // 匹配文字带 <mark> 高亮、每行显示大小/日期；底部是快捷键
  // 提示、没有“关闭”按钮；↑/↓ 移动高亮行，回车打开它。
  if (process.env.VISUAL_MOUNTS) {
    await page.locator('.app-header .input').click()
    await page.waitForTimeout(300)
    await page.keyboard.type('行程')
    await page.waitForTimeout(500)
    const live = await page.evaluate(() => {
      const rows = [...document.querySelectorAll('.palette-row')]
      const paletteText = document.querySelector('.palette')?.textContent ?? ''
      return {
        rows: rows.length,
        firstText: rows[0]?.textContent ?? '',
        hasMark: rows.some((r) => !!r.querySelector('mark')),
        markText: rows[0]?.querySelector('mark')?.textContent ?? '',
        // formatSize(123456) = 121 KB（≥100 取整，见 lib/format.ts）。
        hasSizeDate: /121 KB/.test(rows[0]?.textContent ?? '') && /2026-11-20/.test(rows[0]?.textContent ?? ''),
        // 底部提示没有独立 class，直接在整个面板文本里找。
        footerHasHints: paletteText.includes('↑↓ 选择') && paletteText.includes('回车 打开') && paletteText.includes('Esc 关闭'),
        hasCloseBtn: [...document.querySelectorAll('.palette button')].some((b) => b.textContent?.trim() === '关闭'),
      }
    })
    console.log('palette live search:', JSON.stringify(live))
    if (live.rows !== 1) failures.push(`输入“行程”后应边输边搜出 1 条结果，实测 ${live.rows} 条`)
    if (!live.hasMark) failures.push('命令面板搜索结果没有 <mark> 高亮')
    if (live.markText !== '行程') failures.push(`高亮的应是“行程”，实测 ${JSON.stringify(live.markText)}`)
    if (!live.hasSizeDate) failures.push(`结果行应显示大小/日期（120.6 KB · 2026-11-20），实测 ${JSON.stringify(live.firstText)}`)
    if (!live.footerHasHints) failures.push('命令面板底部没有快捷键提示（↑↓ 选择 · 回车 打开 · Esc 关闭）')
    if (live.hasCloseBtn) failures.push('命令面板底部不应再有“关闭”按钮')
    await page.screenshot({ path: path.join(SHOTS, '02y-palette-live.png') })
    // 多结果方向键：“.p” 命中 3 条（.pdf/.pptx/.png），下移两次
    // 高亮第三行（封面.png），回车打开它、面板关闭。
    await page.keyboard.press('Control+a')
    await page.keyboard.type('.p')
    await page.waitForTimeout(500)
    const multi = await page.evaluate(() => document.querySelectorAll('.palette-row').length)
    if (multi !== 3) failures.push(`输入“.p”后应有 3 条结果，实测 ${multi}`)
    await page.keyboard.press('ArrowDown')
    await page.keyboard.press('ArrowDown')
    await page.waitForTimeout(200)
    const activeRow = await page.evaluate(() => document.querySelector('.palette-row.active')?.textContent ?? '')
    console.log('palette arrow nav active:', JSON.stringify(activeRow))
    if (!activeRow.includes('封面.png')) failures.push(`按两次 ↓ 后高亮行应是“封面.png”，实测 ${JSON.stringify(activeRow)}`)
    await page.keyboard.press('Enter')
    await page.waitForTimeout(500)
    const paletteGone = await page.evaluate(() => !document.querySelector('.palette'))
    if (!paletteGone) failures.push('方向键选中后按回车，命令面板没有关闭（应打开该文件并关闭）')
    // 无结果态：诚实提示，不编数据。
    await page.locator('.app-header .input').click()
    await page.waitForTimeout(300)
    await page.keyboard.type('zzz-no-match')
    await page.waitForTimeout(500)
    const noResult = await page.evaluate(() => (document.querySelector('.palette')?.textContent ?? '').includes('没有找到'))
    if (!noResult) failures.push('输入无匹配关键词后命令面板没有显示“没有找到”')
    await page.keyboard.press('Escape')
    await page.waitForTimeout(400)
  }

  // 2b. 导航结构（v0.17.1）：顶部导航只剩文件浏览 / 同步任务；
  // 设置在左下角、Agent 卡片下方。点它仍能打开设置页由 §4 的
  // '.nav-item' hasText '设置' 点击覆盖。
  const navLayout = await page.evaluate(() => {
    const topNav = [...document.querySelectorAll('.sidebar nav .nav-item')].map((b) => b.textContent?.trim() ?? '')
    const settingsBtn = [...document.querySelectorAll('.sidebar .nav-item')].find((b) => b.textContent?.trim() === '设置')
    const agentCard = [...document.querySelectorAll('.sidebar .card')].find((c) => (c.textContent ?? '').includes('Agent'))
    const sr = settingsBtn?.getBoundingClientRect()
    const ar = agentCard?.getBoundingClientRect()
    return {
      topNav,
      settingsBelowAgent: !!sr && !!ar && sr.top >= ar.bottom - 1,
    }
  })
  console.log('nav layout:', JSON.stringify(navLayout))
  if (navLayout.topNav.length !== 2) failures.push(`顶部导航应只剩 2 项（文件浏览/同步任务），实测 ${JSON.stringify(navLayout.topNav)}`)
  if (navLayout.topNav.some((t) => t === '设置')) failures.push('顶部导航不应再有“设置”')
  if (!navLayout.settingsBelowAgent) failures.push('“设置”应在 Agent 卡片下方（左下角）')

  // 3. 同步任务页：先列已保存任务（零任务空态）
  await page.locator('.nav-item', { hasText: '同步任务' }).click()
  await page.waitForTimeout(600)
  await page.screenshot({ path: path.join(SHOTS, '03-sync.png') })

  // 3b. 点“创建同步任务”：双栏编辑器应出现在顶部（方案 A）
  await page.getByRole('button', { name: '创建同步任务' }).click()
  await page.waitForTimeout(1200)
  await page.screenshot({ path: path.join(SHOTS, '03b-sync-create.png') })

  // 3b2. 默认排除规则（v0.8.3）：新建任务的过滤规则框默认预
  // 填 9 条规则，每条有对应的人话说明；“恢复默认”与“清空”
  // 按钮都在输入框旁，点了都生效。
  const excludeDefaults = await page.evaluate(() => {
    const ta = document.querySelector('.dual-box textarea')
    const box = document.querySelector('.dual-box')
    return {
      value: ta ? ta.value : null,
      hasReset: !!([...(box?.querySelectorAll('button') ?? [])].find((b) => b.textContent?.trim() === '恢复默认')),
      hasClear: !!([...(box?.querySelectorAll('button') ?? [])].find((b) => b.textContent?.trim().startsWith('清空'))),
      notes: box?.textContent ?? '',
    }
  })
  console.log('exclude defaults:', JSON.stringify({ value: excludeDefaults.value, hasReset: excludeDefaults.hasReset, hasClear: excludeDefaults.hasClear }))
  const wantRules = ['.DS_Store', 'Thumbs.db', 'desktop.ini', '._*', '*.tmp', '~$*', '.git/', 'node_modules/', '*.log']
  for (const r of wantRules) {
    if (!excludeDefaults.value?.split('\n').includes(r)) failures.push(`新建任务的过滤规则默认没有 ${r}`)
  }
  if (!excludeDefaults.notes.includes('macOS 文件夹配置文件')) failures.push('过滤规则没有 .DS_Store 的人话说明')
  if (!excludeDefaults.notes.includes('Office')) failures.push('过滤规则没有 ~$* 的人话说明（Office 临时锁文件）')
  if (!excludeDefaults.notes.includes('Git 仓库')) failures.push('过滤规则没有 .git/ 的人话说明')
  if (!excludeDefaults.hasReset) failures.push('过滤规则旁没有“恢复默认”按钮')
  if (!excludeDefaults.hasClear) failures.push('过滤规则旁没有“清空”按钮')
  // 清空 → 恢复默认，来回都生效。
  await page.getByRole('button', { name: '清空（全部同步）', exact: true }).click()
  await page.waitForTimeout(200)
  const cleared = await page.locator('.dual-box textarea').inputValue()
  if (cleared !== '') failures.push(`点“清空”后过滤规则框应为空，实测 ${JSON.stringify(cleared)}`)
  await page.getByRole('button', { name: '恢复默认', exact: true }).click()
  await page.waitForTimeout(200)
  const restored = await page.locator('.dual-box textarea').inputValue()
  if (!restored.includes('.DS_Store') || !restored.includes('node_modules/')) failures.push('点“恢复默认”后默认规则没有回来')

  // 3c. 双栏点选（VISUAL_MOUNTS 才有网盘可点）：单击一行即选中，
  // 选中行有明显选中态（绿底 + 左绿边；对勾只留左侧复选框那一个，
  // 选中行本身不再画右侧对勾），两栏各自独立；双栏上方回显的
  // 源/目标路径就是选中的路径；双击文件夹进入后，根目录变为进入的文件夹。
  if (process.env.VISUAL_MOUNTS) {
    const srcPane = page.locator('.dual-panes .pane').first()
    await srcPane.locator('.pane-row', { hasText: '行程.pdf' }).click()
    await page.waitForTimeout(600)
    const selCheck = await page.evaluate(() => {
      const panes = [...document.querySelectorAll('.dual-panes .pane')]
      const src = panes[0]
      const dst = panes[1]
      const sel = src?.querySelector('.pane-row.selected')
      const echo = document.querySelector('.sync-sel-echo')?.textContent ?? ''
      return {
        srcSelectedName: sel?.textContent ?? '',
        paneCheckCount: document.querySelectorAll('.dual-panes .pane-check').length,
        dstHasSelection: !!dst?.querySelector('.pane-row.selected'),
        echo,
      }
    })
    console.log('dual-pane selection:', JSON.stringify(selCheck))
    if (!selCheck.srcSelectedName.includes('行程.pdf')) failures.push('双栏单击“行程.pdf”后该行没有选中态（.pane-row.selected）')
    if (selCheck.paneCheckCount !== 0) failures.push(`双栏里不应再有右侧对勾（.pane-check），实测 ${selCheck.paneCheckCount} 个`)
    if (selCheck.dstHasSelection) failures.push('点源栏一行后目标栏也被选中了（两栏选中态必须各自独立）')
    if (!selCheck.echo.includes('行程.pdf')) failures.push(`双栏上方回显的源路径没有选中的“行程.pdf”，实测 ${JSON.stringify(selCheck.echo)}`)
    await page.screenshot({ path: path.join(SHOTS, '03c-sync-select.png') })
    // 双击文件夹进入：根目录变为该文件夹，回显随之变化。
    await srcPane.locator('.pane-row', { hasText: '照片' }).dblclick()
    await page.waitForTimeout(600)
    const enterCheck = await page.evaluate(() => {
      const echo = document.querySelector('.sync-sel-echo')?.textContent ?? ''
      const src = [...document.querySelectorAll('.dual-panes .pane')][0]
      return { echo, stillSelected: !!src?.querySelector('.pane-row.selected') }
    })
    console.log('dual-pane enter folder:', JSON.stringify(enterCheck))
    if (!enterCheck.echo.includes('照片')) failures.push(`双击“照片”进入后回显的源路径没有“照片”，实测 ${JSON.stringify(enterCheck.echo)}`)
    if (enterCheck.stillSelected) failures.push('双击进入文件夹后旧的选中行还留在选中态（进入后应以进入的文件夹为根）')
    await page.screenshot({ path: path.join(SHOTS, '03d-sync-enter-folder.png') })

    // 3c2. 双栏多选（v0.8.3）：复选框多选、已选数量与清单、清空
    // 选择、Shift 连选、全选本页；多选态（复选框打勾 + 淡底）
    // 必须与单选根（绿底 + 左绿边 + 勾选标记）区分开；点复选
    // 框不能同时把行选成同步根。批量创建：每项一个任务。
    const multiState = () =>
      page.evaluate(() => {
        const panes = [...document.querySelectorAll('.dual-panes .pane')]
        const src = panes[0]
        const rows = [...(src?.querySelectorAll('.pane-row') ?? [])]
        const multi = rows.filter((r) => r.classList.contains('multiselected'))
        const text = src?.textContent ?? ''
        const first = multi[0] ? getComputedStyle(multi[0]) : null
        return {
          count: multi.length,
          summary: /已选 \d+ 项/.test(text) ? (text.match(/已选 \d+ 项/) ?? [''])[0] : null,
          names: multi.map((r) => r.textContent ?? ''),
          selectedRows: rows.filter((r) => r.classList.contains('selected')).length,
          bg: first?.backgroundColor ?? null,
          hasGreenEdge: first ? first.boxShadow.includes('86, 225, 108') : false,
        }
      })
    // 勾两项：行程.pdf、预算.xlsx。
    await srcPane.locator('.pane-row', { hasText: '行程.pdf' }).locator('.row-checkbox').click()
    await page.waitForTimeout(300)
    await srcPane.locator('.pane-row', { hasText: '预算.xlsx' }).locator('.row-checkbox').click()
    await page.waitForTimeout(300)
    let ms = await multiState()
    console.log('multi 2:', JSON.stringify(ms))
    if (ms.count !== 2) failures.push(`勾了 2 个复选框，多选应为 2 项，实测 ${ms.count}`)
    if (ms.summary !== '已选 2 项') failures.push(`多选 2 项后清单应显示“已选 2 项”，实测 ${JSON.stringify(ms.summary)}`)
    if (!ms.names.some((n) => n.includes('行程.pdf')) || !ms.names.some((n) => n.includes('预算.xlsx'))) {
      failures.push(`多选清单里应有“行程.pdf”和“预算.xlsx”，实测 ${JSON.stringify(ms.names)}`)
    }
    if (ms.selectedRows !== 0) failures.push('点复选框后行被选成了同步根（复选框只应多选，不应动单选根）')
    if (ms.bg !== 'rgb(238, 244, 255)') failures.push(`多选行底色应为淡蓝灰 rgb(238, 244, 255)，与单选根的淡绿区分开，实测 ${ms.bg}`)
    if (ms.hasGreenEdge) failures.push('多选行不应有单选根的左绿边（两者必须区分开）')
    // Shift 连选：上次勾的是预算.xlsx，Shift 点封面.png，
    // 应连选预算.xlsx、分享.pptx、封面.png，加上行程.pdf 共 4 项。
    await srcPane.locator('.pane-row', { hasText: '封面.png' }).locator('.row-checkbox').click({ modifiers: ['Shift'] })
    await page.waitForTimeout(300)
    ms = await multiState()
    console.log('multi shift:', JSON.stringify(ms))
    if (ms.count !== 4) failures.push(`Shift 连选后多选应为 4 项，实测 ${ms.count}`)
    if (ms.summary !== '已选 4 项') failures.push(`Shift 连选后清单应显示“已选 4 项”，实测 ${JSON.stringify(ms.summary)}`)
    // 全选本页：13 行全选（v0.14.0：桩列表多了 说明.md，从 12 变 13）。
    await srcPane.locator('label', { hasText: '全选本页' }).click()
    await page.waitForTimeout(300)
    ms = await multiState()
    console.log('multi select-all:', JSON.stringify(ms))
    if (ms.count !== 13) failures.push(`全选本页后多选应为 13 项，实测 ${ms.count}`)
    // 清空选择：清单消失。
    await srcPane.getByRole('button', { name: '清空选择', exact: true }).click()
    await page.waitForTimeout(300)
    ms = await multiState()
    if (ms.count !== 0) failures.push(`点“清空选择”后多选应为 0 项，实测 ${ms.count}`)
    if (ms.summary !== null) failures.push('点“清空选择”后“已选 N 项”清单还显示着')
    // 批量创建：再勾 2 项（行程.pdf、预算.xlsx），填任务名，
    // 点“创建 2 个任务”，列表里应出现 2 个任务（每项一个）。
    await srcPane.locator('.pane-row', { hasText: '行程.pdf' }).locator('.row-checkbox').click()
    await page.waitForTimeout(200)
    await srcPane.locator('.pane-row', { hasText: '预算.xlsx' }).locator('.row-checkbox').click()
    await page.waitForTimeout(300)
    await page.locator('.dual-box .field input').first().fill('批量测试')
    await page.waitForTimeout(300)
    const batchEcho = await page.locator('.sync-sel-echo').textContent()
    if (!batchEcho?.includes('将创建 2 个任务')) failures.push(`多选 2 项后回显应说“将创建 2 个任务”，实测 ${JSON.stringify(batchEcho)}`)
    await page.screenshot({ path: path.join(SHOTS, '03e-sync-multiselect.png') })
    await page.getByRole('button', { name: '创建 2 个任务', exact: true }).click()
    await page.waitForTimeout(800)
    const batchResult = await page.evaluate(() => {
      const cards = [...document.querySelectorAll('.job-card')].map((c) => c.textContent ?? '')
      const info = document.querySelector('.notice-box')?.textContent ?? ''
      return { cards, info }
    })
    console.log('batch create:', JSON.stringify(batchResult))
    if (!batchResult.cards.some((t) => t.includes('批量测试 — 行程.pdf'))) failures.push('批量创建后列表里没有“批量测试 — 行程.pdf”任务')
    if (!batchResult.cards.some((t) => t.includes('批量测试 — 预算.xlsx'))) failures.push('批量创建后列表里没有“批量测试 — 预算.xlsx”任务')
    if (!batchResult.info.includes('已创建 2 个同步任务')) failures.push('批量创建后没有“已创建 2 个同步任务”的提示')
    await page.screenshot({ path: path.join(SHOTS, '03f-sync-batch-created.png') })
    await page.locator('.dual-box >> text=取消').click().catch(() => {})
    await page.waitForTimeout(400)

    // 3f2. 定时 + 运行记录（V0.9.0）：编辑器里有“定时”选择
    // （手动 / 每小时 / 每天固定时间跑），选每天后出现时间
    // 输入；任务卡片上有“定时：手动”和“运行记录”按钮，点开
    // 显示真实的空记录（桩返回 []），不是假数据。
    await page.getByRole('button', { name: '创建同步任务' }).click()
    await page.waitForTimeout(800)
    const sched = await page.evaluate(() => {
      const box = document.querySelector('.dual-box')
      if (!box) return { found: false }
      const text = box.textContent ?? ''
      const selects = [...box.querySelectorAll('select')].map((s) => [...s.options].map((o) => o.textContent))
      return { found: true, hasManual: text.includes('只在点“立即同步”时跑'), hasHourly: text.includes('每小时跑一次'), hasDaily: text.includes('每天固定时间跑'), selects }
    })
    console.log('schedule ui:', JSON.stringify(sched))
    if (!sched.hasManual || !sched.hasHourly || !sched.hasDaily) failures.push(`编辑器里“定时”三选项不全（手动/每小时/每天），实测 ${JSON.stringify(sched)}`)
    // 选每天 → 时间输入出现。
    await page.locator('.dual-box select').last().selectOption({ label: '每天固定时间跑' })
    await page.waitForTimeout(300)
    const dailyTime = await page.evaluate(() => {
      const box = document.querySelector('.dual-box')
      return { hasTimeInput: !!box?.querySelector('input[type="time"]') }
    })
    if (!dailyTime.hasTimeInput) failures.push('定时选“每天固定时间跑”后没有出现时间输入')
    await page.screenshot({ path: path.join(SHOTS, '03g-sync-schedule.png') })
    await page.locator('.dual-box >> text=取消').click().catch(() => {})
    await page.waitForTimeout(400)
    // 任务卡片：定时文案 + 运行记录空态。
    const jobMeta = await page.evaluate(() => {
      const cards = [...document.querySelectorAll('.job-card')].map((c) => c.textContent ?? '')
      return { cards, hasSchedule: cards.some((t) => t.includes('定时：手动')), hasRunsBtn: cards.some((t) => t.includes('运行记录')) }
    })
    console.log('job card schedule:', JSON.stringify(jobMeta))
    if (!jobMeta.hasSchedule) failures.push('任务卡片上没有“定时：手动”')
    if (!jobMeta.hasRunsBtn) failures.push('任务卡片上没有“运行记录”按钮')
    if (jobMeta.hasRunsBtn) {
      await page.getByRole('button', { name: '运行记录', exact: true }).first().click()
      await page.waitForTimeout(600)
      const runs = await page.evaluate(() => {
        const cards = [...document.querySelectorAll('.job-card')].map((c) => c.textContent ?? '')
        return { hasEmpty: cards.some((t) => t.includes('还没有运行记录')), hasTitle: cards.some((t) => t.includes('运行记录')) }
      })
      if (!runs.hasEmpty) failures.push(`点“运行记录”后没有显示空记录，实测 ${JSON.stringify(runs)}`)
    }
    await page.screenshot({ path: path.join(SHOTS, '03h-sync-runs.png') })

    // 3f3. runNow 忙态 + 0 项守卫（v0.17.1）：点任务卡片的
    // “立即同步”，预览变慢的 900ms 里按钮必须禁用并显示
    // “同步中…”，重复点也不能再发一次预览；预览回来是空
    // 计划，runNow 应拦截、不调 RunSync、不记空运行记录，报
    // “没有需要同步的内容”，按钮恢复可用。
    await page.evaluate(() => { window.__slowPreview = true })
    await page.waitForTimeout(1200) // 让编辑器残留的自动重算先落袋
    const callsBefore = await page.evaluate(() => window.__previewSyncCalls || 0)
    await page.getByRole('button', { name: '立即同步', exact: true }).first().click()
    await page.waitForTimeout(250)
    const busyState = await page.evaluate(() => {
      const btn = [...document.querySelectorAll('.job-card .btn-primary')].find((b) => b.textContent?.includes('同步中') || b.textContent?.includes('立即同步'))
      return btn ? { disabled: btn.disabled, text: btn.textContent?.trim() ?? '' } : { disabled: null, text: '' }
    })
    console.log('runNow busy:', JSON.stringify(busyState))
    if (!busyState.disabled || busyState.text !== '同步中…') failures.push(`点“立即同步”后按钮应禁用并显示“同步中…”，实测 disabled=${busyState.disabled} text=${JSON.stringify(busyState.text)}`)
    // 按钮已禁用，Playwright 点不动它——用桩计数证明重复点
    // 没有多发预览：跑完后总调用只应涨 1。
    await page.waitForFunction(
      () => [...document.querySelectorAll('.notice-box')].some((el) => el.textContent?.includes('没有需要同步的内容')),
      { timeout: 5000 },
    )
    const callsAfter = await page.evaluate(() => window.__previewSyncCalls || 0)
    const idleState = await page.evaluate(() => {
      const btn = [...document.querySelectorAll('.job-card .btn-primary')].find((b) => b.textContent?.includes('立即同步'))
      return btn ? { disabled: btn.disabled, text: btn.textContent?.trim() ?? '' } : { disabled: null, text: '' }
    })
    console.log('runNow idle:', JSON.stringify({ callsBefore, callsAfter, ...idleState }))
    if (callsAfter !== callsBefore + 1) failures.push(`“立即同步”一次只应触发 1 次预览，实测调用从 ${callsBefore} 涨到 ${callsAfter}`)
    if (idleState.disabled || idleState.text !== '立即同步') failures.push(`跑完后按钮应恢复可用并显示“立即同步”，实测 disabled=${idleState.disabled} text=${JSON.stringify(idleState.text)}`)
    await page.evaluate(() => { window.__slowPreview = false })
    await page.screenshot({ path: path.join(SHOTS, '03i-sync-runnow.png') })
  }

  // 4. 设置页：Agent 列表（零 Agent 空态）
  // 4a. 动效中间态：点过去 150ms 时页面过渡应还在跑（500ms 基础
  // 曲线），opacity 必须在 0 和 1 之间——瞬间硬切会被这项拦下来。
  await page.locator('.nav-item', { hasText: '设置' }).click()
  await page.waitForTimeout(150)
  const midTransition = await page.evaluate(() => {
    const el = document.querySelector('.page-enter')
    if (!el) return { found: false }
    const cs = getComputedStyle(el)
    const btn = document.querySelector('.btn-primary')
    const bcs = btn ? getComputedStyle(btn) : null
    return {
      found: true,
      opacity: parseFloat(cs.opacity),
      pageDuration: cs.animationDuration,
      pageCurve: cs.animationTimingFunction,
      btnCurve: bcs?.transitionTimingFunction,
      btnDurations: bcs?.transitionDuration,
    }
  })
  console.log('mid-transition:', JSON.stringify(midTransition))
  await page.screenshot({ path: path.join(SHOTS, '04-mid-transition.png') })
  if (!midTransition.found) failures.push('找不到 .page-enter（页面过渡容器）')
  if (midTransition.found && !(midTransition.opacity > 0.05 && midTransition.opacity < 0.95)) {
    failures.push(`页面过渡中间态 opacity 应在 0~1 之间，实测 ${midTransition.opacity}（疑似瞬间硬切）`)
  }
  // 4b. 动效配置审计：关键元素的曲线必须是基础曲线
  // cubic-bezier(0.22,1,0.36,1)，页面过渡 500ms，hover 颜色 300ms /
  // transform 200ms；绝不出现浏览器默认 ease/linear。
  const EASE = 'cubic-bezier(0.22, 1, 0.36, 1)'
  if (midTransition.found) {
    if (midTransition.pageDuration !== '0.5s') failures.push(`页面过渡时长应为 500ms，实测 ${midTransition.pageDuration}`)
    if (midTransition.pageCurve !== EASE) failures.push(`页面过渡曲线应为基础曲线，实测 ${midTransition.pageCurve}`)
    // transition-timing-function 是逗号分隔的列表，但每个
    // cubic-bezier(...) 内部也有逗号，所以按整段 bezier 匹配，
    // 不能按 ', ' 切分。
    if (midTransition.btnCurve) {
      const curves = midTransition.btnCurve.match(/cubic-bezier\([^)]*\)|[a-z-]+/g) ?? []
      const bad = curves.filter((c) => c !== EASE)
      if (bad.length) failures.push(`主按钮 transition 曲线应全是基础曲线，实测 ${midTransition.btnCurve}`)
    }
    if (midTransition.btnDurations && !midTransition.btnDurations.includes('0.3s')) {
      failures.push(`主按钮 transition 时长应含 300ms（颜色），实测 ${midTransition.btnDurations}`)
    }
  }
  await page.waitForTimeout(600)
  await page.screenshot({ path: path.join(SHOTS, '04-settings.png') })

  // 4b2. 设置页分组卡片 + 分段选择断言：每节一张卡（背景 --card、
  // 圆角 16px、节间距 24px、顶部 3px 品牌绿条），节标题 18px 加粗
  // （Drime 式，本页特批）+ flat-color-icons 小图标，标题下有分割线；
  // 互斥选项是分段选择（主题/语言/代理/比较方式/冲突策略/缓存上限），
  // 激活段是白色 pill；次要控件收进“高级”折叠（通用：开机启动；
  // 同步默认：并发与限速）。
  const settingsCards = await page.evaluate(() => {
    const cards = [...document.querySelectorAll('.settings-card')]
    const cardInfo = cards.map((c) => {
      const cs = getComputedStyle(c)
      const r = c.getBoundingClientRect()
      const head = c.querySelector('.settings-card-head')
      const h2 = head?.querySelector('h2')
      const hcs = h2 ? getComputedStyle(h2) : null
      const headCs = head ? getComputedStyle(head) : null
      return {
        bg: cs.backgroundColor,
        radius: cs.borderTopLeftRadius,
        marginBottom: cs.marginBottom,
        borderTopWidth: cs.borderTopWidth,
        borderTopColor: cs.borderTopColor,
        w: Math.round(r.width),
        h2Size: hcs?.fontSize ?? null,
        h2Weight: hcs?.fontWeight ?? null,
        hasIcon: !!head?.querySelector('svg'),
        hasDivider: headCs ? headCs.borderBottomWidth !== '0px' : false,
      }
    })
    const segInfo = [...document.querySelectorAll('.settings-card .segmented')].map((g) => {
      const btns = [...g.querySelectorAll('.segmented-btn')]
      const active = btns.find((b) => b.getAttribute('aria-pressed') === 'true')
      const acs = active ? getComputedStyle(active) : null
      return {
        label: g.getAttribute('aria-label'),
        count: btns.length,
        activeLabel: active?.textContent?.trim() ?? null,
        activeBg: acs?.backgroundColor ?? null,
        activeShadow: acs?.boxShadow ?? null,
      }
    })
    return {
      cardCount: cards.length,
      cardInfo,
      segInfo,
      advancedCount: document.querySelectorAll('.settings-card .settings-advanced').length,
      detailsCount: document.querySelectorAll('.settings-panel details').length,
      hasAdvancedLabel: [...document.querySelectorAll('.settings-panel')].some((p) => (p.textContent ?? '').includes('高级：')),
    }
  })
  console.log('settings cards:', JSON.stringify(settingsCards))
  if (settingsCards.cardCount < 7) failures.push(`设置页分组卡片应至少 7 张（通用/网络/网盘/同步默认/缓存/Agent/关于），实测 ${settingsCards.cardCount} 张`)
  for (const c of settingsCards.cardInfo) {
    if (c.bg !== 'rgb(245, 245, 245)') failures.push(`设置卡片背景应为 --card #F5F5F5，实测 ${c.bg}`)
    if (c.radius !== '16px') failures.push(`设置卡片圆角应为 16px，实测 ${c.radius}`)
    if (c.marginBottom !== '24px') failures.push(`设置卡片节间距应为 24px，实测 ${c.marginBottom}`)
    if (c.borderTopWidth !== '3px' || c.borderTopColor !== 'rgb(86, 225, 108)') {
      failures.push(`设置卡片顶部应有 3px 品牌绿标识，实测 ${c.borderTopWidth} ${c.borderTopColor}`)
    }
    if (c.h2Size !== '18px') failures.push(`设置节标题字号应为 18px，实测 ${c.h2Size}`)
    if (c.h2Weight !== '700') failures.push(`设置节标题字重应为 700（Drime 式加粗，本页特批），实测 ${c.h2Weight}`)
    if (!c.hasIcon) failures.push('设置节标题旁没有 flat-color-icons 小图标')
    if (!c.hasDivider) failures.push('设置节标题下没有分割线')
  }
  if (settingsCards.advancedCount !== 0) failures.push(`设置页不应再有“高级”折叠（v0.17.1 已改为常显行），实测 ${settingsCards.advancedCount} 处`)
  if (settingsCards.detailsCount !== 0) failures.push(`设置页不应再有 <details>，实测 ${settingsCards.detailsCount} 个`)
  if (settingsCards.hasAdvancedLabel) failures.push('设置页不应再出现“高级：”文案')
  // 分段选择（v0.17.0）：主题/语言/代理/比较方式/冲突策略/缓存上限共 6 组，
  // 每组恰一个激活段，激活段是白色 pill（边框 + 轻阴影）。
  if (settingsCards.segInfo.length < 6) failures.push(`设置页分段选择应至少 6 组，实测 ${settingsCards.segInfo.length} 组`)
  for (const g of settingsCards.segInfo) {
    if (!g.activeLabel) failures.push(`分段选择“${g.label}”没有激活段`)
    if (g.activeBg !== 'rgb(255, 255, 255)') failures.push(`分段选择“${g.label}”激活段应是白色 pill，实测 ${g.activeBg}`)
    if (!g.activeShadow || g.activeShadow === 'none') failures.push(`分段选择“${g.label}”激活段应有轻阴影`)
  }

  // 4b2t. 设置页顶栏 tabs（Drime 式，v0.17.0）：七节横排 pill 组，
  // 激活项是白色 pill（边框 + 轻阴影）；一次只显示一节；切 tab
  // 时新内容按方向滑入（向右切从右侧进）、旧内容向反方向滑出；
  // 未保存的编辑切走再切回还在；缓存开关是 Drime 式 switch。
  const tabsInfo = await page.evaluate(() => {
    const list = document.querySelector('.settings-tabs[role="tablist"]')
    const tabs = [...document.querySelectorAll('.settings-tabs [role="tab"]')]
    const active = tabs.find((tb) => tb.getAttribute('aria-selected') === 'true')
    const acs = active ? getComputedStyle(active) : null
    const visiblePanels = [...document.querySelectorAll('.settings-panel')].filter((p) => getComputedStyle(p).display !== 'none')
    return {
      hasList: !!list,
      labels: tabs.map((tb) => tb.textContent?.trim()),
      activeLabel: active?.textContent?.trim() ?? null,
      activeBg: acs?.backgroundColor ?? null,
      activeShadow: acs?.boxShadow ?? null,
      panelCount: document.querySelectorAll('.settings-panel').length,
      visiblePanelCount: visiblePanels.length,
      visiblePanelId: visiblePanels[0]?.id ?? null,
    }
  })
  console.log('settings tabs:', JSON.stringify(tabsInfo))
  if (!tabsInfo.hasList) failures.push('设置页没有顶栏 tab 组（.settings-tabs[role="tablist"]）')
  const EXPECT_TABS = ['通用', '网络', '网盘', '同步默认', '缓存', 'Agent', '关于']
  if (JSON.stringify(tabsInfo.labels) !== JSON.stringify(EXPECT_TABS)) failures.push(`设置顶栏应为七节 ${EXPECT_TABS.join('/')}，实测 ${JSON.stringify(tabsInfo.labels)}`)
  if (tabsInfo.activeLabel !== '通用') failures.push(`默认激活的 tab 应为“通用”，实测 ${tabsInfo.activeLabel}`)
  if (tabsInfo.activeBg !== 'rgb(255, 255, 255)') failures.push(`激活 tab 应是白色 pill，实测背景 ${tabsInfo.activeBg}`)
  if (!tabsInfo.activeShadow || tabsInfo.activeShadow === 'none') failures.push('激活 tab 应有轻阴影')
  if (tabsInfo.panelCount !== 7) failures.push(`设置面板应为 7 个（七节各一），实测 ${tabsInfo.panelCount}`)
  if (tabsInfo.visiblePanelCount !== 1 || tabsInfo.visiblePanelId !== 'settings-panel-general') failures.push(`一次只应显示一节（通用），实测可见 ${tabsInfo.visiblePanelCount} 个（${tabsInfo.visiblePanelId}）`)
  // 未保存的编辑：在通用填下载文件夹（不保存），切走再切回还在。
  await page.locator('#settings-panel-general .input').first().fill('/tmp/zy-drive-draft-keep')
  // 向右切（通用→缓存）：350ms/40px 滑动，新内容应从右侧滑入、
  // 旧内容向左滑出；80ms 处新内容 tx 应已明显大于 0（约 11px，旧 220ms/24px
  // 时只有约 4px、看不到过渡），旧内容 tx 应明显小于 0（约 -29px）；
  // 落定后只有缓存可见。
  await page.getByRole('tab', { name: '缓存', exact: true }).click()
  await page.waitForTimeout(80)
  const slideRight = await page.evaluate(() => {
    const parseTx = (el) => {
      if (!el) return null
      const tr = getComputedStyle(el).transform
      const m = tr.match(/matrix\([^,]+,[^,]+,[^,]+,[^,]+,\s*([-\d.]+)/)
      return m ? parseFloat(m[1]) : null
    }
    const entering = document.querySelector('#settings-panel-cache')
    const leaving = document.querySelector('#settings-panel-general')
    return {
      enterTx: parseTx(entering),
      enterOpacity: entering ? parseFloat(getComputedStyle(entering).opacity) : null,
      leaveTx: parseTx(leaving),
      enterDur: entering ? getComputedStyle(entering).animationDuration : null,
      enterCurve: entering ? getComputedStyle(entering).animationTimingFunction : null,
    }
  })
  console.log('slide right mid-animation:', JSON.stringify(slideRight))
  if (slideRight.enterTx === null || slideRight.enterTx <= 4 || slideRight.enterTx > 40) failures.push(`向右切时新内容应从右侧明显滑入（80ms 处 tx 应在 4~40 之间，约 11px），实测 ${slideRight.enterTx}`)
  if (slideRight.leaveTx === null || slideRight.leaveTx >= -20 || slideRight.leaveTx < -40) failures.push(`向右切时旧内容应向左明显滑出（80ms 处 tx 应在 -40~-20 之间，约 -29px），实测 ${slideRight.leaveTx}`)
  if (slideRight.enterOpacity !== null && !(slideRight.enterOpacity > 0 && slideRight.enterOpacity < 1)) failures.push(`滑动中途新内容 opacity 应在 0~1 之间，实测 ${slideRight.enterOpacity}`)
  if (slideRight.enterDur !== '0.35s') failures.push(`滑动时长应为 350ms，实测 ${slideRight.enterDur}`)
  if (slideRight.enterCurve !== 'cubic-bezier(0.22, 1, 0.36, 1)') failures.push(`滑动曲线应为基础曲线，实测 ${slideRight.enterCurve}`)
  await page.screenshot({ path: path.join(SHOTS, '04b0d-settings-slide-mid.png') })
  await page.waitForTimeout(400)
  const cacheVisible = await page.evaluate(() => {
    const panels = [...document.querySelectorAll('.settings-panel')].filter((p) => getComputedStyle(p).display !== 'none')
    const cache = document.querySelector('#settings-panel-cache')
    return { visible: panels.map((p) => p.id), cacheText: cache?.textContent ?? '' }
  })
  if (JSON.stringify(cacheVisible.visible) !== JSON.stringify(['settings-panel-cache'])) failures.push(`落定后应只显示缓存一节，实测 ${JSON.stringify(cacheVisible.visible)}`)
  if (!cacheVisible.cacheText.includes('1.2 MB')) failures.push('切到缓存后没有显示缓存占用（桩 1.2 MB）')
  await page.screenshot({ path: path.join(SHOTS, '04b0-settings-tabs-cache.png') })
  // 开关（Drime 式）：退出时清理缓存是 role=switch，44×24，关时灰底；
  // 点一下开（轨道变品牌绿），再点一下恢复关（草稿不保存，不影响后文）。
  const switchInfo = await page.evaluate(() => {
    const sw = document.querySelector('#settings-panel-cache [role="switch"]')
    if (!sw) return { found: false }
    const cs = getComputedStyle(sw)
    const r = sw.getBoundingClientRect()
    const knob = sw.querySelector('.switch-knob')
    const kcs = knob ? getComputedStyle(knob) : null
    return {
      found: true,
      checked: sw.getAttribute('aria-checked'),
      w: Math.round(r.width),
      h: Math.round(r.height),
      trackBg: cs.backgroundColor,
      knobBg: kcs?.backgroundColor ?? null,
    }
  })
  console.log('cache switch:', JSON.stringify(switchInfo))
  if (!switchInfo.found) failures.push('缓存卡里没有开关（退出时清理缓存应是 Drime 式 switch）')
  if (switchInfo.found && switchInfo.checked !== 'false') failures.push(`退出时清理缓存默认应关，实测 aria-checked=${switchInfo.checked}`)
  if (switchInfo.found && (switchInfo.w !== 44 || switchInfo.h !== 24)) failures.push(`开关应为 44×24，实测 ${switchInfo.w}×${switchInfo.h}`)
  if (switchInfo.found && switchInfo.trackBg !== 'rgb(200, 200, 200)') failures.push(`开关关时轨道应为灰色 rgb(200, 200, 200)，实测 ${switchInfo.trackBg}`)
  if (switchInfo.found && switchInfo.knobBg !== 'rgb(255, 255, 255)') failures.push(`开关钮应为白色，实测 ${switchInfo.knobBg}`)
  if (switchInfo.found) {
    await page.locator('#settings-panel-cache [role="switch"]').click()
    await page.waitForTimeout(300)
    const on = await page.evaluate(() => {
      const sw = document.querySelector('#settings-panel-cache [role="switch"]')
      return { checked: sw?.getAttribute('aria-checked') ?? null, trackBg: sw ? getComputedStyle(sw).backgroundColor : null }
    })
    if (on.checked !== 'true' || on.trackBg !== 'rgb(86, 225, 108)') failures.push(`点开关后应开（aria-checked=true，轨道品牌绿），实测 ${JSON.stringify(on)}`)
    await page.locator('#settings-panel-cache [role="switch"]').click()
    await page.waitForTimeout(300)
    const off = await page.evaluate(() => document.querySelector('#settings-panel-cache [role="switch"]')?.getAttribute('aria-checked'))
    if (off !== 'false') failures.push(`再点开关后应恢复关，实测 ${off}`)
  }
  // 向左切（缓存→通用）：新内容应从左侧滑入（tx < 0），旧内容向右滑出。
  await page.getByRole('tab', { name: '通用', exact: true }).click()
  await page.waitForTimeout(80)
  const slideLeft = await page.evaluate(() => {
    const parseTx = (el) => {
      if (!el) return null
      const tr = getComputedStyle(el).transform
      const m = tr.match(/matrix\([^,]+,[^,]+,[^,]+,[^,]+,\s*([-\d.]+)/)
      return m ? parseFloat(m[1]) : null
    }
    const entering = document.querySelector('#settings-panel-general')
    const leaving = document.querySelector('#settings-panel-cache')
    return { enterTx: parseTx(entering), leaveTx: parseTx(leaving) }
  })
  console.log('slide left mid-animation:', JSON.stringify(slideLeft))
  if (slideLeft.enterTx === null || slideLeft.enterTx >= -4 || slideLeft.enterTx < -40) failures.push(`向左切时新内容应从左侧明显滑入（80ms 处 tx 应在 -40~-4 之间，约 -11px），实测 ${slideLeft.enterTx}`)
  if (slideLeft.leaveTx === null || slideLeft.leaveTx <= 20 || slideLeft.leaveTx > 40) failures.push(`向左切时旧内容应向右明显滑出（80ms 处 tx 应在 20~40 之间，约 29px），实测 ${slideLeft.leaveTx}`)
  await page.waitForTimeout(400)
  const draftKept = await page.evaluate(() => {
    const input = document.querySelector('#settings-panel-general .input')
    return { value: input?.value ?? null }
  })
  if (draftKept.value !== '/tmp/zy-drive-draft-keep') failures.push(`切走再切回后未保存的下载文件夹编辑应还在，实测 ${JSON.stringify(draftKept.value)}`)
  await page.screenshot({ path: path.join(SHOTS, '04b0b-settings-tabs-general.png') })
  // 通用“开机启动”已改为常显行（v0.17.1：不再有“高级”折叠）：不点任何东西
  // 就应看到开机启动行（桩里 GetOpenAtLogin 抛错，显示诚实说明而非假开关）。
  const loginRow = await page.evaluate(() => {
    const panel = document.querySelector('#settings-panel-general')
    const rows = [...(panel?.querySelectorAll('.settings-row') ?? [])]
    const row = rows.find((r) => (r.textContent ?? '').includes('开机启动'))
    return { found: !!row, text: row?.textContent ?? '' }
  })
  if (!loginRow.found) failures.push('通用里没有常显的“开机启动”行（高级折叠已移除，应直接显示）')
  if (loginRow.found && !loginRow.text.includes('暂不支持')) failures.push(`开机启动行应显示诚实说明（桩里开不了机启动），实测 ${JSON.stringify(loginRow.text)}`)
  await page.screenshot({ path: path.join(SHOTS, '04b0c-settings-general-login.png') })
  // 同步默认的并发与限速已改为常显行（v0.17.1）：切到该 tab 后不点任何东西
  // 就应看到两行（各带一个 number 输入框，桩值 4 与 0）。
  await page.getByRole('tab', { name: '同步默认', exact: true }).click()
  await page.waitForTimeout(500)
  const syncRows = await page.evaluate(() => {
    const panel = document.querySelector('#settings-panel-sync')
    if (!panel) return { found: false }
    const rows = [...panel.querySelectorAll('.settings-row')]
    const vis = (el) => !!el && getComputedStyle(el).display !== 'none' && el.getBoundingClientRect().height > 0
    const conc = rows.find((r) => (r.textContent ?? '').includes('默认并发数'))
    const limit = rows.find((r) => (r.textContent ?? '').includes('限速'))
    return {
      found: true,
      hasConcurrency: vis(conc),
      hasLimit: vis(limit),
      concurrencyValue: conc?.querySelector('input')?.value ?? null,
      limitValue: limit?.querySelector('input')?.value ?? null,
    }
  })
  console.log('sync default rows:', JSON.stringify(syncRows))
  if (!syncRows.found) failures.push('没有同步默认面板')
  if (syncRows.found && !syncRows.hasConcurrency) failures.push('同步默认里没有常显的“默认并发数”行（高级折叠已移除，应直接显示）')
  if (syncRows.found && !syncRows.hasLimit) failures.push('同步默认里没有常显的“限速”行（高级折叠已移除，应直接显示）')
  if (syncRows.found && syncRows.concurrencyValue !== '4') failures.push(`默认并发数应为桩的 4，实测 ${syncRows.concurrencyValue}`)
  if (syncRows.found && syncRows.limitValue !== '0') failures.push(`限速应为桩的 0，实测 ${syncRows.limitValue}`)
  await page.screenshot({ path: path.join(SHOTS, '04b0e-settings-sync-defaults.png') })

  // 4b3. 设置页“网络”区（v0.8.5）：三选一（跟随系统代理默认 /
  // 手动设置 / 直连）+ 检测到的系统代理一行 + 手动地址字段 +
  // “测试代理”按钮。桩的系统代理是 127.0.0.1:7890。
  // 切 tab 后再操作：平时藏起来的节是 display:none，先切到对应 tab。
  await page.getByRole('tab', { name: '网络', exact: true }).click()
  await page.waitForTimeout(350)
  const networkSection = await page.evaluate(() => {
    const card = [...document.querySelectorAll('.settings-card')].find((c) => {
      const h2 = c.querySelector('h2')
      return h2?.textContent?.trim() === '网络'
    })
    if (!card) return { found: false }
    const text = card.textContent ?? ''
    const seg = card.querySelector('.segmented[aria-label="代理"]')
    const segBtns = seg ? [...seg.querySelectorAll('.segmented-btn')] : []
    const activeSeg = segBtns.find((b) => b.getAttribute('aria-pressed') === 'true')
    return {
      found: true,
      hasSystem: segBtns.some((b) => b.textContent?.trim() === '跟随系统代理'),
      hasManual: segBtns.some((b) => b.textContent?.trim() === '手动设置'),
      hasDirect: segBtns.some((b) => b.textContent?.trim() === '直连'),
      systemChecked: activeSeg?.textContent?.trim() === '跟随系统代理',
      detectedLine: text.includes('系统代理：http://127.0.0.1:7890'),
      hasTestBtn: [...card.querySelectorAll('button')].some((b) => (b.textContent ?? '').trim() === '测试代理'),
      hasManualFields: text.includes('HTTP 代理'),
    }
  })
  console.log('settings network:', JSON.stringify(networkSection))
  if (!networkSection.found) failures.push('设置页没有“网络”区（v0.8.5 应新增）')
  if (networkSection.found) {
    if (!networkSection.hasSystem || !networkSection.hasManual || !networkSection.hasDirect) failures.push('网络区三选一不完整（跟随系统代理 / 手动设置 / 直连）')
    if (networkSection.systemChecked !== true) failures.push('网络区默认应选中“跟随系统代理”')
    if (!networkSection.detectedLine) failures.push('网络区没有显示检测到的系统代理（桩：http://127.0.0.1:7890）')
    if (!networkSection.hasTestBtn) failures.push('网络区没有“测试代理”按钮')
    if (networkSection.hasManualFields) failures.push('手动地址字段在“跟随系统代理”模式下不应显示')
    // 切到手动设置：地址字段出现，填 HTTP 代理、保存、测试。
    await page.getByRole('button', { name: '手动设置', exact: true }).click()
    await page.waitForTimeout(300)
    const manualShown = await page.evaluate(() => {
      const card = [...document.querySelectorAll('.settings-card')].find((c) => c.querySelector('h2')?.textContent?.trim() === '网络')
      return card?.textContent?.includes('HTTP 代理') ?? false
    })
    if (!manualShown) failures.push('切到“手动设置”后没有出现 HTTP 代理地址字段')
    await page.locator('.settings-card', { hasText: '网络' }).locator('input[placeholder*="127.0.0.1:7890"]').first().fill('http://127.0.0.1:7890')
    await page.getByRole('button', { name: '保存代理设置', exact: true }).click()
    await page.waitForTimeout(300)
    await page.getByRole('button', { name: '测试代理', exact: true }).click()
    await page.waitForTimeout(300)
    const proxyTest = await page.evaluate(() => {
      const card = [...document.querySelectorAll('.settings-card')].find((c) => c.querySelector('h2')?.textContent?.trim() === '网络')
      return card?.textContent?.includes('连通') ?? false
    })
    if (!proxyTest) failures.push('点“测试代理”后没有显示连通结果（桩应返回经代理连通）')
    const networkCard = page.locator('.settings-card').nth(1)
    await networkCard.screenshot({ path: path.join(SHOTS, '04b-settings-network.png') })
  }

  // 4b2a. 设置页缓存卡（v0.9.1）：显示当前占用 + 文件数 + 上限
  // （桩：1.2 MB、3 个文件、上限 2 GB）、上限选择框默认 2 GB、
  // “退出时清理缓存”开关、点“清理缓存”后提示释放了多少。
  {
    // 切 tab 后再操作：平时藏起来的节是 display:none，先切到对应 tab。
    await page.getByRole('tab', { name: '缓存', exact: true }).click()
    await page.waitForTimeout(350)
    const cacheCard = await page.evaluate(() => {
      const card = [...document.querySelectorAll('.settings-card')].find((c) => c.querySelector('h2')?.textContent?.trim() === '缓存')
      if (!card) return { found: false }
      const seg = card.querySelector('.segmented[aria-label="缓存上限"]')
      const activeSeg = seg?.querySelector('.segmented-btn[aria-pressed="true"]')
      return {
        found: true,
        text: card.textContent ?? '',
        limitValue: activeSeg?.textContent?.trim() ?? null,
        hasClearBtn: [...card.querySelectorAll('button')].some((b) => b.textContent?.trim() === '清理缓存'),
        hasExitToggle: !!card.querySelector('[role="switch"][aria-label="退出时清理缓存"]'),
      }
    })
    console.log('cache card:', JSON.stringify(cacheCard))
    if (!cacheCard.found) failures.push('设置页里没有“缓存”卡')
    if (cacheCard.found && !cacheCard.text.includes('1.2 MB')) failures.push(`缓存卡没有显示当前占用“1.2 MB”（桩 PreviewCacheInfo.sizeBytes=1234567），实测 ${JSON.stringify(cacheCard.text.slice(0, 200))}`)
    if (cacheCard.found && !cacheCard.text.includes('3 个文件')) failures.push('缓存卡没有显示文件数“3 个文件”')
    if (cacheCard.found && cacheCard.limitValue !== '2 GB') failures.push(`缓存上限默认激活段应是 2 GB，实测 ${cacheCard.limitValue}`)
    if (cacheCard.found && !cacheCard.hasClearBtn) failures.push('缓存卡里没有“清理缓存”按钮')
    if (cacheCard.found && !cacheCard.hasExitToggle) failures.push('缓存卡里没有“退出时清理缓存”开关')
    if (cacheCard.found && cacheCard.hasClearBtn) {
      await page.locator('.settings-card', { hasText: '缓存' }).getByRole('button', { name: '清理缓存', exact: true }).click()
      await page.waitForTimeout(500)
      const cleared = await page.evaluate(() => {
        const card = [...document.querySelectorAll('.settings-card')].find((c) => c.querySelector('h2')?.textContent?.trim() === '缓存')
        return card?.textContent ?? ''
      })
      if (!cleared.includes('已清理预览缓存，释放了 1.2 MB（3 个文件）')) failures.push(`点“清理缓存”后没有释放提示，实测 ${JSON.stringify(cleared.slice(0, 200))}`)
      await page.locator('.settings-card', { hasText: '缓存' }).screenshot({ path: path.join(SHOTS, '04b2a-settings-cache.png') })
    }
  }

  // 4b2b. 设置页 S3 “修改桶 / 前缀”（问题 1，VISUAL_MOUNTS）：
  // 网盘行显示的是 rootLabel（桶名），不是旧的 rootRef；S3 行有
  // “修改桶 / 前缀”入口，点开预填当前桶名。
  if (process.env.VISUAL_MOUNTS) {
    // 切 tab 后再操作：平时藏起来的节是 display:none，先切到对应 tab。
    await page.getByRole('tab', { name: '网盘', exact: true }).click()
    await page.waitForTimeout(350)
    const editRoot = await page.evaluate(() => {
      const section = [...document.querySelectorAll('.settings-card')].find((c) => c.textContent?.includes('已添加的网盘'))
      if (!section) return { found: false }
      const text = section.textContent ?? ''
      const btn = [...section.querySelectorAll('button')].find((b) => b.textContent.trim() === '修改桶 / 前缀')
      return { found: true, showsBucketLabel: text.includes('test-bucket'), hasEditButton: !!btn }
    })
    console.log('settings edit-root:', JSON.stringify(editRoot))
    if (!editRoot.found) failures.push('设置页找不到网盘一节')
    if (editRoot.found && !editRoot.showsBucketLabel) failures.push('设置页网盘行没有显示桶名 rootLabel（test-bucket）')
    if (editRoot.found && !editRoot.hasEditButton) failures.push('设置页 S3 网盘行没有“修改桶 / 前缀”入口')
    if (editRoot.hasEditButton) {
      // 两个 S3 网盘（我的 R2 + AWS 备份）各有一个同名按钮：只点
      // 第一个（我的 R2，test-bucket），否则 strict mode 会报 2 个元素。
      await page.locator('.settings-card', { hasText: '已添加的网盘' }).getByRole('button', { name: '修改桶 / 前缀' }).first().click()
      await page.waitForTimeout(400)
      const rootDialog = await page.evaluate(() => {
        const d = [...document.querySelectorAll('.dialog')].find((el) => el.textContent?.includes('修改桶 / 前缀'))
        if (!d) return { found: false }
        const inputs = [...d.querySelectorAll('input')].map((i) => i.value)
        return { found: true, inputs, hasHint: d.textContent?.includes('不要把桶名再填一遍') ?? false }
      })
      console.log('edit-root dialog:', JSON.stringify(rootDialog))
      if (!rootDialog.found) failures.push('点“修改桶 / 前缀”后没有弹出对话框')
      if (rootDialog.found && rootDialog.inputs?.[0] !== 'test-bucket') failures.push(`修改桶对话框应预填当前桶名 test-bucket，实测 ${JSON.stringify(rootDialog.inputs)}`)
      if (rootDialog.found && !rootDialog.hasHint) failures.push('修改桶对话框没有“不要把桶名再填一遍”的提示')
      await page.screenshot({ path: path.join(SHOTS, '04d-edit-root.png') })
      if (rootDialog.found) {
        await page.locator('.dialog-actions >> text=取消').click()
        await page.waitForTimeout(400)
      }
    }
  }

  // 4e. 设置页“重新测试”（C，VISUAL_MOUNTS）：未验证的网盘
  // （本地照片桩）行里有“未验证”徽章和“重新测试”按钮；点它
  // 后测试通过、徽章消失（桩的 TestMount 把 verified 翻 true，
  // ListMounts 返回 live 列表）。
  if (process.env.VISUAL_MOUNTS) {
    // 切 tab 后再操作：平时藏起来的节是 display:none，先切到对应 tab。
    await page.getByRole('tab', { name: '网盘', exact: true }).click()
    await page.waitForTimeout(350)
    const retestBefore = await page.evaluate(() => {
      const section = [...document.querySelectorAll('.settings-card')].find((c) => c.textContent?.includes('已添加的网盘'))
      if (!section) return { found: false }
      const row = [...section.querySelectorAll('.agent-row')].find((r) => r.textContent?.includes('本地照片'))
      if (!row) return { found: false }
      return {
        found: true,
        hasBadge: row.textContent?.includes('未验证') ?? false,
        hasButton: [...row.querySelectorAll('button')].some((b) => b.textContent.trim() === '重新测试'),
      }
    })
    console.log('settings retest before:', JSON.stringify(retestBefore))
    if (!retestBefore.found) failures.push('设置页找不到“本地照片”网盘行（应为未验证桩）')
    if (retestBefore.found && !retestBefore.hasBadge) failures.push('设置页未验证的网盘行没有“未验证”徽章')
    if (retestBefore.found && !retestBefore.hasButton) failures.push('设置页未验证的网盘行没有“重新测试”按钮')
    if (retestBefore.hasButton) {
      const row = page.locator('.settings-card', { hasText: '已添加的网盘' }).locator('.agent-row', { hasText: '本地照片' })
      await row.getByRole('button', { name: '重新测试', exact: true }).click()
      await page.waitForTimeout(800)
      const retestAfter = await page.evaluate(() => {
        const section = [...document.querySelectorAll('.settings-card')].find((c) => c.textContent?.includes('已添加的网盘'))
        const row = [...(section?.querySelectorAll('.agent-row') ?? [])].find((r) => r.textContent?.includes('本地照片'))
        if (!row) return { found: false }
        return { found: true, hasBadge: row.textContent?.includes('未验证') ?? false, msg: row.textContent ?? '' }
      })
      console.log('settings retest after:', JSON.stringify(retestAfter))
      if (!retestAfter.msg.includes('测试通过')) failures.push('点“重新测试”后没有显示“测试通过”')
      if (retestAfter.hasBadge) failures.push('重新测试通过后“未验证”徽章没有消失')
      await page.screenshot({ path: path.join(SHOTS, '04e-settings-retest.png') })
    }
  }

  // 4b4. Agent 对话框与导出配置包（V0.9.0）：添加 Agent 对话框
  // 里有 API Token 字段（留空 = 不改），不再有“V0.2 接入”的
  // 旧提示；保存后 Agent 行上有“导出配置包”，填口令导出后
  // 显示文件路径。
  // 切 tab 后再操作：平时藏起来的节是 display:none，先切到对应 tab。
  await page.getByRole('tab', { name: 'Agent', exact: true }).click()
  await page.waitForTimeout(350)
  await page.getByRole('button', { name: '添加 Agent', exact: true }).click()
  await page.waitForTimeout(400)
  const agentDialog = await page.evaluate(() => {
    const d = [...document.querySelectorAll('.dialog')].find((el) => el.textContent?.includes('添加 Agent'))
    if (!d) return { found: false }
    const text = d.textContent ?? ''
    return {
      found: true,
      hasToken: text.includes('API Token'),
      hasV02: text.includes('V0.2'),
      hasExportHint: text.includes('导出配置包'),
    }
  })
  console.log('agent dialog:', JSON.stringify(agentDialog))
  if (!agentDialog.found) failures.push('点“添加 Agent”后没有弹出对话框')
  if (agentDialog.found && !agentDialog.hasToken) failures.push('Agent 对话框里没有 API Token 字段（V0.9.0 应新增）')
  if (agentDialog.found && agentDialog.hasV02) failures.push('Agent 对话框里还留着“V0.2 接入”的旧提示')
  if (agentDialog.found && !agentDialog.hasExportHint) failures.push('Agent 对话框里没有提“导出配置包”')
  if (agentDialog.found) {
    await page.locator('.dialog input').nth(0).fill('测试服务器')
    await page.locator('.dialog input').nth(1).fill('127.0.0.1:18080')
    await page.locator('.dialog input[type="password"]').fill('stub-agent-token')
    await page.locator('.dialog').getByRole('button', { name: '保存', exact: true }).click()
    await page.waitForTimeout(800)
    const agentRow = await page.evaluate(() => {
      const section = [...document.querySelectorAll('.settings-card')].find((c) => c.querySelector('h2')?.textContent?.trim() === 'Agent')
      if (!section) return { found: false }
      const text = section.textContent ?? ''
      return { found: true, hasRow: text.includes('测试服务器'), hasExport: [...section.querySelectorAll('button')].some((b) => b.textContent.trim() === '导出配置包') }
    })
    console.log('agent row:', JSON.stringify(agentRow))
    if (!agentRow.hasRow) failures.push('保存 Agent 后列表里没有“测试服务器”')
    if (agentRow.hasRow && !agentRow.hasExport) failures.push('Agent 行上没有“导出配置包”按钮')
    if (agentRow.hasExport) {
      await page.getByRole('button', { name: '导出配置包', exact: true }).click()
      await page.waitForTimeout(400)
      await page.locator('.settings-card input[type="password"]').fill('export-pass-123')
      await page.locator('.settings-card', { hasText: '测试服务器' }).getByRole('button', { name: '导出', exact: true }).click()
      await page.waitForTimeout(600)
      const exportOut = await page.evaluate(() => {
        const section = [...document.querySelectorAll('.settings-card')].find((c) => c.querySelector('h2')?.textContent?.trim() === 'Agent')
        const text = section?.textContent ?? ''
        return { hasPath: text.includes('zy-drive-agent-bundle-ag-1-20261125-000000.json'), hasImportHint: text.includes('zy-drive-agent import') }
      })
      console.log('agent export:', JSON.stringify(exportOut))
      if (!exportOut.hasPath) failures.push('点“导出”后没有显示配置包文件路径')
      if (!exportOut.hasImportHint) failures.push('导出后没有提示服务器上的导入命令')
      await page.screenshot({ path: path.join(SHOTS, '04f-settings-agent-export.png') })
    }
  }

  // 4b5. 原地重新授权（V0.9.0，VISUAL_MOUNTS）：点“重新授权”
  // 后对话框标题是“重新授权：<名字>”，走完保存后还是同一个
  // 网盘（侧栏里没有多出一个），任务/分组/缓存都在——不是新建。
  // 注意：按名字找“我的 R2”那行点——2i/2j 拖拽改了顺序，
  // 不能再假设列表第一行是 S3 网盘（v0.11.0 修）。
  if (process.env.VISUAL_MOUNTS) {
    // 切 tab 后再操作：平时藏起来的节是 display:none，先切到对应 tab。
    await page.getByRole('tab', { name: '网盘', exact: true }).click()
    await page.waitForTimeout(350)
    const sidebarBefore = await page.locator('.sidebar').textContent()
    const mountCountBefore = (sidebarBefore?.match(/我的 R2/g) ?? []).length
    await page.locator('.settings-card', { hasText: '已添加的网盘' }).locator('.agent-row', { hasText: '我的 R2' }).getByRole('button', { name: '重新授权' }).click()
    await page.waitForTimeout(500)
    const reauthDialog = await page.evaluate(() => {
      const d = document.querySelector('.dialog')
      if (!d) return { found: false }
      return { found: true, title: d.querySelector('h1')?.textContent ?? '', text: d.textContent ?? '' }
    })
    console.log('reauth dialog:', JSON.stringify(reauthDialog))
    if (!reauthDialog.found) failures.push('点“重新授权”后没有弹出对话框')
    if (reauthDialog.found && !reauthDialog.title.startsWith('重新授权：')) failures.push(`重新授权对话框标题应为“重新授权：<名字>”，实测 ${JSON.stringify(reauthDialog.title)}`)
    if (reauthDialog.found && !reauthDialog.text.includes('还是这个网盘')) failures.push('重新授权对话框没有说明“还是这个网盘”（原地更新）')
    if (reauthDialog.found) {
      // S3 表单：填桶与密钥，测试通过后保存。
      await page.locator('.dialog .field', { hasText: '桶名（Bucket）' }).locator('input').fill('test-bucket')
      await page.locator('.dialog .field', { hasText: 'AccessKey' }).locator('input').fill('ak-reauth')
      await page.locator('.dialog .field', { hasText: 'SecretKey' }).locator('input').fill('sk-reauth')
      await page.locator('.dialog').getByRole('button', { name: '测试连接', exact: true }).click()
      await page.waitForTimeout(600)
      const testOut = await page.evaluate(() => document.querySelector('.dialog')?.textContent ?? '')
      if (!testOut.includes('连接测试通过')) failures.push('重新授权时测试连接没有通过（桩应返回通过）')
      await page.locator('.dialog-actions').getByRole('button', { name: '保存', exact: true }).click()
      await page.waitForTimeout(800)
      const sidebarAfter = await page.locator('.sidebar').textContent()
      const mountCountAfter = (sidebarAfter?.match(/我的 R2/g) ?? []).length
      if (mountCountAfter !== mountCountBefore || mountCountAfter !== 1) failures.push(`重新授权后“我的 R2”在侧栏里应仍为 1 个（原地更新，不新建），保存前 ${mountCountBefore} 个、保存后 ${mountCountAfter} 个`)
      await page.screenshot({ path: path.join(SHOTS, '04g-settings-reauth.png') })
    }
  }

  // 4b3. 字体审计（2026-11-25 去加粗试验）：Schibsted Grotesk 400
  // 必须本地加载成功，全站字重统一 400，不得残留 550/500。
  const fontAudit = await page.evaluate(async () => {
    try {
      await document.fonts.load('400 16px "Schibsted Grotesk"', 'ZyDrive')
    } catch {
      // 加载失败时 document.fonts.check 会报出来，不在这里吞掉
    }
    const loaded = document.fonts.check('400 16px "Schibsted Grotesk"')
    const h1 = document.querySelector('h1')
    const btn = document.querySelector('.btn-primary')
    const res = await fetch('/fonts/schibsted-grotesk-latin-400.woff2')
    return {
      loaded,
      fontFileOk: res.ok,
      h1Weight: h1 ? getComputedStyle(h1).fontWeight : null,
      h1Family: h1 ? getComputedStyle(h1).fontFamily : null,
      btnWeight: btn ? getComputedStyle(btn).fontWeight : null,
    }
  })
  console.log('font audit:', JSON.stringify(fontAudit))
  if (!fontAudit.fontFileOk) failures.push('字体文件 /fonts/schibsted-grotesk-latin-400.woff2 打不开（应随 dist 一起发布）')
  if (!fontAudit.loaded) failures.push('Schibsted Grotesk 400 没有加载成功（document.fonts.check 为 false），页面仍在用 fallback 字体')
  if (fontAudit.h1Weight !== '400') failures.push(`h1 字重应为 400，实测 ${fontAudit.h1Weight}`)
  if (fontAudit.btnWeight !== '400') failures.push(`主按钮字重应为 400，实测 ${fontAudit.btnWeight}`)
  if (fontAudit.h1Family && !fontAudit.h1Family.includes('Schibsted Grotesk')) failures.push(`h1 字体族应以 Schibsted Grotesk 开头，实测 ${fontAudit.h1Family}`)

  // 4c. 深色主题：data-theme="dark" 后底色必须真的变深。
  await page.evaluate(() => {
    document.documentElement.dataset.theme = 'dark'
  })
  // 等 400ms：.settings-tab 的 background-color 有 220ms 过渡，
  // 200ms 量会在过渡中途抓到中间色（曾实测 rgb(25, 28, 24)）。
  await page.waitForTimeout(400)
  const darkBg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  console.log('dark body background:', darkBg)
  if (darkBg !== 'rgb(23, 26, 22)') failures.push(`深色主题下页面底色应为 rgb(23, 26, 22)，实测 ${darkBg}`)
  const darkSettings = await page.evaluate(() => {
    const tab = document.querySelector('.settings-tab-active')
    const card = document.querySelector('.settings-card')
    return {
      tabBg: tab ? getComputedStyle(tab).backgroundColor : null,
      cardBg: card ? getComputedStyle(card).backgroundColor : null,
    }
  })
  console.log('dark settings:', JSON.stringify(darkSettings))
  if (darkSettings.tabBg !== 'rgb(23, 26, 22)') failures.push(`深色下激活 tab 应为纸面色 rgb(23, 26, 22)，实测 ${darkSettings.tabBg}`)
  if (darkSettings.cardBg !== 'rgb(34, 40, 35)') failures.push(`深色下设置卡片应为 rgb(34, 40, 35)，实测 ${darkSettings.cardBg}`)
  await page.screenshot({ path: path.join(SHOTS, '04c-settings-dark.png') })
  await page.evaluate(() => {
    document.documentElement.dataset.theme = 'light'
  })

  // 5. 横向溢出自查：整页横向滚动条即为布局问题；同时量顶栏按钮
  // 是否被裁（右缘超出视口即为问题）
  const overflow = await page.evaluate(() => {
    const doc = document.documentElement
    const headerBtn = [...document.querySelectorAll('.app-header button')].pop()?.getBoundingClientRect()
    const sidebar = document.querySelector('.sidebar')?.getBoundingClientRect()
    return {
      scrollW: doc.scrollWidth,
      clientW: doc.clientWidth,
      overflowX: doc.scrollWidth > doc.clientWidth,
      headerBtnRight: headerBtn ? Math.round(headerBtn.right) : null,
      viewportW: window.innerWidth,
      sidebarLeft: sidebar ? Math.round(sidebar.left) : null,
    }
  })
  console.log('page overflow:', JSON.stringify(overflow))
  if (overflow.overflowX) failures.push(`页面横向溢出：scrollWidth ${overflow.scrollW} > clientWidth ${overflow.clientW}`)
  if (overflow.headerBtnRight !== null && overflow.headerBtnRight > overflow.viewportW) {
    failures.push(`顶栏按钮右缘超出视口：right=${overflow.headerBtnRight} > viewport=${overflow.viewportW}`)
  }

  // 6. 不注桩的图：Bind 失败时的人话错误态
  const page2 = await browser.newPage({ viewport: { width: 1280, height: 800 } })
  await page2.goto(`http://127.0.0.1:${port}/`, { waitUntil: 'networkidle' })
  await page2.waitForTimeout(800)
  await page2.screenshot({ path: path.join(SHOTS, '05-browse-no-stub.png') })
  await page2.close()
} finally {
  await browser.close()
  server.close()
}

if (failures.length) {
  console.error('VISUAL CHECK FAILED:')
  for (const f of failures) console.error(' -', f)
  process.exit(1)
}
console.log(`VISUAL CHECK OK — shots in ${SHOTS}`)
