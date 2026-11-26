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

const MIME = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png' }

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
  { id: 'm-s3', displayName: '我的 R2', provider: 's3', accountId: 'a1', rootRef: '', rootLabel: 'test-bucket', endpointHost: 'abc123.r2.cloudflarestorage.com', group: '工作', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
  { id: 'm-s3aws', displayName: 'AWS 备份', provider: 's3', accountId: 'a4', rootRef: '', rootLabel: 'backup-bucket', endpointHost: 's3.ap-northeast-1.amazonaws.com', group: '存储 / 备份', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
  { id: 'm-db', displayName: 'Dropbox', provider: 'dropbox', accountId: 'a2', rootRef: '', rootLabel: '', group: '个人', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
  { id: 'm-dr', displayName: '工作 Drive', provider: 'drive', accountId: 'a3', rootRef: '', rootLabel: '', group: '工作', caps: { ...CAPS, idBased: true }, status: 'auth_expired', verified: true, createdAt: '', updatedAt: '' },
  { id: 'm-od', displayName: 'OneDrive', provider: 'onedrive', accountId: 'a5', rootRef: '', rootLabel: '', group: '个人', caps: { ...CAPS, idBased: true }, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
  { id: 'm-drime', displayName: 'Drime', provider: 'drime', accountId: 'a6', rootRef: '', rootLabel: '', workspaceLabel: '工作区 8762', group: '个人', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
  { id: 'm-local', displayName: '本地照片', provider: 'local', accountId: 'a7', rootRef: '/Users/zy/照片', rootLabel: '/Users/zy/照片', group: '存储 / 备份', caps: CAPS, status: 'ok', verified: false, createdAt: '', updatedAt: '' },
  // 超长网盘名（30 个汉字）：悬停删除按钮时名字必须仍是单行
  // 省略、不换行；它的容量查询失败，侧栏应显示“容量暂不可用”。
  { id: 'm-long', displayName: '这是一个特别特别长的网盘名字用来测试省略号是否正常工作啊啊啊', provider: 'dropbox', accountId: 'a8', rootRef: '', rootLabel: '', group: '个人', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
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
        const m = { id: 'm-new', displayName: input.displayName || '新网盘', provider: input.provider, accountId: 'a-new', rootRef: '', rootLabel: input.bucket || '', group: input.group || '', caps: ${JSON.stringify(CAPS)}, status: 'ok', verified: !input.skipProbe, createdAt: '', updatedAt: '' }
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
      if (method === 'DesktopService.PreviewSync') return { jobId: '', direction: 'copy', items: [], filteredOut: 0, hasDeletes: false }
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
      if (method === 'DesktopService.CheckDownloadDir') return null
      if (method === 'DesktopService.GetVersions') return { backend: '0.3.0', protocol: '1', engine: '0.1.0' }
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
      const sel = document.querySelector('.dialog select')
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

  // 2e. 网盘卡片的删除按钮（VISUAL_MOUNTS）：平时隐藏，悬停卡片
  // 才出现；必须是内联 SVG 图标，不是 emoji；hover 变危险红。
  if (process.env.VISUAL_MOUNTS) {
    const mountRemove = await page.evaluate(() => {
      const card = document.querySelector('.mount-card')
      const btn = card?.querySelector('.mount-remove')
      if (!card || !btn) return { found: false }
      const cs = getComputedStyle(btn)
      return {
        found: true,
        displayBefore: cs.display,
        hasSvg: !!btn.querySelector('svg'),
        text: btn.textContent.trim(),
        title: btn.getAttribute('title'),
      }
    })
    console.log('mount remove (before hover):', JSON.stringify(mountRemove))
    if (!mountRemove.found) failures.push('找不到网盘卡片的删除按钮（.mount-remove）')
    if (mountRemove.found && mountRemove.displayBefore !== 'none') failures.push(`网盘删除按钮平时应隐藏，实测 display=${mountRemove.displayBefore}`)
    if (mountRemove.found && !mountRemove.hasSvg) failures.push('网盘删除按钮应是内联 SVG 图标，不是 emoji/文字')
    if (mountRemove.found && mountRemove.text !== '') failures.push(`网盘删除按钮里不应有文字/emoji，实测 ${JSON.stringify(mountRemove.text)}`)
    if (mountRemove.found && mountRemove.title !== '移除网盘') failures.push(`网盘删除按钮 title 应为“移除网盘”，实测 ${JSON.stringify(mountRemove.title)}`)
    await page.hover('.mount-card').catch(() => failures.push('悬停网盘卡片失败'))
    await page.waitForTimeout(300)
    const mountRemoveHover = await page.evaluate(() => {
      const btn = document.querySelector('.mount-card .mount-remove')
      if (!btn) return { found: false }
      const cs = getComputedStyle(btn)
      return { found: true, displayAfter: cs.display }
    })
    console.log('mount remove (on hover):', JSON.stringify(mountRemoveHover))
    if (mountRemoveHover.found && mountRemoveHover.displayAfter === 'none') failures.push('悬停网盘卡片后删除按钮没有出现')
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
        const btn = card.querySelector('.mount-remove')
        if (!name || !btn) return { found: false }
        const nr = name.getBoundingClientRect()
        const cs = getComputedStyle(name)
        const bcs = getComputedStyle(btn)
        return {
          found: true,
          singleLine: nr.height <= 22,
          ellipsis: cs.textOverflow === 'ellipsis' && cs.whiteSpace === 'nowrap' && cs.overflow === 'hidden',
          clipped: name.scrollWidth > name.clientWidth,
          removeVisible: bcs.display !== 'none',
          removeAbsolute: bcs.position === 'absolute',
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
      if (longCheck.found && !longCheck.removeAbsolute) failures.push('网盘删除按钮应为绝对定位（不占布局），实测不是 absolute')
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
    await page.locator('.file-table input[type="checkbox"]').first().click()
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
    await page.locator('.file-table input[type="checkbox"]').first().click()
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

  // 2i. 长按拖拽换分组（v0.10.0，VISUAL_MOUNTS）：按住“工作”
  // 组的第一张网盘卡片（我的 R2）450ms 进入拖拽态
  // （.mount-card.dragging），拖到“个人”分组行松手 →
  // UpdateMountGroup 被调到（mountId + 新分组），卡片随之换组。
  // 注意：分组按名字 Unicode 排序，“个人”排在最前——不能
  // 拿侧栏第一张卡片当源（它已在“个人”组，拖过去是同组 no-op）。
  if (process.env.VISUAL_MOUNTS) {
    const firstCard = page.locator('[data-group="工作"] .mount-card').first()
    // “工作”组按 Unicode 排序在最后，卡片可能在侧栏可视区
    // 之外：先滚到可见，否则按住点落在视口外、拖拽起不来。
    await firstCard.scrollIntoViewIfNeeded()
    const draggedName = (await firstCard.locator('.mount-name').textContent())?.trim() ?? ''
    const cardBox = await firstCard.boundingBox()
    const personalGroup = page.locator('[data-group="个人"]')
    const groupBox = await personalGroup.boundingBox()
    if (!cardBox || !groupBox) {
      failures.push('长按拖拽测试：找不到网盘卡片或“个人”分组行')
    } else {
      await page.mouse.move(cardBox.x + cardBox.width / 2, cardBox.y + cardBox.height / 2)
      await page.mouse.down()
      await page.waitForTimeout(450)
      const dragging = await page.evaluate(() => !!document.querySelector('.mount-card.dragging'))
      if (!dragging) failures.push('按住网盘卡片 450ms 后没有进入拖拽态（.mount-card.dragging）')
      await page.screenshot({ path: path.join(SHOTS, '02i-drag-group.png') })
      // 分步移动到目标分组行中心（每步都经过 pointermove，
      // 拖拽态的 dropGroup 才能跟上）。
      const steps = 10
      for (let i = 1; i <= steps; i++) {
        await page.mouse.move(
          cardBox.x + cardBox.width / 2 + (groupBox.x + groupBox.width / 2 - (cardBox.x + cardBox.width / 2)) * i / steps,
          cardBox.y + cardBox.height / 2 + (groupBox.y + groupBox.height / 2 - (cardBox.y + cardBox.height / 2)) * i / steps,
        )
        await page.waitForTimeout(30)
      }
      await page.mouse.up()
      await page.waitForTimeout(600)
      const lastGroup = await page.evaluate(() => window.__lastGroupUpdate ?? null)
      console.log('last group update:', JSON.stringify(lastGroup))
      if (!lastGroup || lastGroup[1] !== '个人') failures.push(`拖到“个人”分组后 UpdateMountGroup 没被调到，实测 ${JSON.stringify(lastGroup)}`)
      const personalText = await page.evaluate(() => document.querySelector('[data-group="个人"]')?.textContent ?? '')
      if (draggedName && !personalText.includes(draggedName)) failures.push(`拖拽后“${draggedName}”没出现在“个人”分组里`)
    }
  }

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
  // 选中行有明显选中态（绿底 + 左绿边 + 勾选标记），两栏各自
  // 独立；双栏上方回显的源/目标路径就是选中的路径；双击文件
  // 夹进入后，根目录变为进入的文件夹。
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
        srcHasCheck: !!sel?.querySelector('.pane-check svg'),
        dstHasSelection: !!dst?.querySelector('.pane-row.selected'),
        echo,
      }
    })
    console.log('dual-pane selection:', JSON.stringify(selCheck))
    if (!selCheck.srcSelectedName.includes('行程.pdf')) failures.push('双栏单击“行程.pdf”后该行没有选中态（.pane-row.selected）')
    if (!selCheck.srcHasCheck) failures.push('双栏选中行没有勾选标记（.pane-check svg）')
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
    // 全选本页：12 行全选。
    await srcPane.locator('label', { hasText: '全选本页' }).click()
    await page.waitForTimeout(300)
    ms = await multiState()
    console.log('multi select-all:', JSON.stringify(ms))
    if (ms.count !== 12) failures.push(`全选本页后多选应为 12 项，实测 ${ms.count}`)
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

  // 4b2. 设置页分组卡片 + 选择框断言：每节一张卡（背景 --card、
  // 圆角 16px、节间距 24px、顶部 3px 品牌绿条），选择框统一样式
  // （自定义箭头、固定高度 40px、圆角 8px）；节标题 18px +
  // flat-color-icons 小图标，标题下有分割线；次要控件收进
  // “高级”折叠（通用：开机启动；同步默认：并发与限速）。
  const settingsCards = await page.evaluate(() => {
    const cards = [...document.querySelectorAll('.settings-card')]
    const selects = [...document.querySelectorAll('.settings-card select')]
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
    const selectInfo = selects.map((s) => {
      const cs = getComputedStyle(s)
      const r = s.getBoundingClientRect()
      return {
        h: Math.round(r.height),
        radius: cs.borderTopLeftRadius,
        appearance: cs.appearance,
        hasArrow: cs.backgroundImage.includes('svg'),
        disabled: s.disabled,
      }
    })
    return {
      cardCount: cards.length,
      cardInfo,
      selectCount: selects.length,
      selectInfo,
      advancedCount: document.querySelectorAll('.settings-card .settings-advanced').length,
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
    if (c.h2Weight !== '400') failures.push(`设置节标题字重应保持 400，实测 ${c.h2Weight}`)
    if (!c.hasIcon) failures.push('设置节标题旁没有 flat-color-icons 小图标')
    if (!c.hasDivider) failures.push('设置节标题下没有分割线')
  }
  if (settingsCards.advancedCount < 2) failures.push(`设置页“高级”折叠应至少 2 处（通用开机启动、同步默认并发与限速），实测 ${settingsCards.advancedCount} 处`)
  if (settingsCards.selectCount === 0) failures.push('设置页里找不到选择框（.settings-card select）')
  for (const s of settingsCards.selectInfo) {
    if (s.h < 38 || s.h > 42) failures.push(`选择框高度应为 40px 左右，实测 ${s.h}px`)
    if (s.radius !== '8px') failures.push(`选择框圆角应为 8px，实测 ${s.radius}`)
    if (s.appearance !== 'none') failures.push(`选择框应去掉浏览器默认外观（appearance: none），实测 ${s.appearance}`)
    if (!s.hasArrow) failures.push('选择框没有自定义下拉箭头（background-image 应含 SVG）')
  }

  // 4b3. 设置页“网络”区（v0.8.5）：三选一（跟随系统代理默认 /
  // 手动设置 / 直连）+ 检测到的系统代理一行 + 手动地址字段 +
  // “测试代理”按钮。桩的系统代理是 127.0.0.1:7890。
  const networkSection = await page.evaluate(() => {
    const card = [...document.querySelectorAll('.settings-card')].find((c) => {
      const h2 = c.querySelector('h2')
      return h2?.textContent?.trim() === '网络'
    })
    if (!card) return { found: false }
    const text = card.textContent ?? ''
    const radios = [...card.querySelectorAll('input[type="radio"]')].map((r) => ({ checked: r.checked }))
    return {
      found: true,
      hasSystem: text.includes('跟随系统代理'),
      hasManual: text.includes('手动设置'),
      hasDirect: text.includes('直连'),
      systemChecked: radios[0]?.checked ?? null,
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
    await page.locator('.settings-card', { hasText: '网络' }).getByRole('radio', { name: '手动设置' }).click()
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
    const cacheCard = await page.evaluate(() => {
      const card = [...document.querySelectorAll('.settings-card')].find((c) => c.querySelector('h2')?.textContent?.trim() === '缓存')
      if (!card) return { found: false }
      const select = card.querySelector('select')
      return {
        found: true,
        text: card.textContent ?? '',
        limitValue: select?.value ?? null,
        hasClearBtn: [...card.querySelectorAll('button')].some((b) => b.textContent?.trim() === '清理缓存'),
        hasExitToggle: [...card.querySelectorAll('label')].some((l) => l.textContent?.includes('退出时清理缓存')),
      }
    })
    console.log('cache card:', JSON.stringify(cacheCard))
    if (!cacheCard.found) failures.push('设置页里没有“缓存”卡')
    if (cacheCard.found && !cacheCard.text.includes('1.2 MB')) failures.push(`缓存卡没有显示当前占用“1.2 MB”（桩 PreviewCacheInfo.sizeBytes=1234567），实测 ${JSON.stringify(cacheCard.text.slice(0, 200))}`)
    if (cacheCard.found && !cacheCard.text.includes('3 个文件')) failures.push('缓存卡没有显示文件数“3 个文件”')
    if (cacheCard.found && cacheCard.limitValue !== '2048') failures.push(`缓存上限选择框默认应是 2 GB（2048），实测 ${cacheCard.limitValue}`)
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
  if (process.env.VISUAL_MOUNTS) {
    const sidebarBefore = await page.locator('.sidebar').textContent()
    const mountCountBefore = (sidebarBefore?.match(/我的 R2/g) ?? []).length
    await page.locator('.settings-card', { hasText: '已添加的网盘' }).getByRole('button', { name: '重新授权' }).first().click()
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
  await page.waitForTimeout(200)
  const darkBg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  console.log('dark body background:', darkBg)
  if (darkBg !== 'rgb(23, 26, 22)') failures.push(`深色主题下页面底色应为 rgb(23, 26, 22)，实测 ${darkBg}`)
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
    failures.push(`顶栏“新建同步”按钮右缘超出视口：right=${overflow.headerBtnRight} > viewport=${overflow.viewportW}`)
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
