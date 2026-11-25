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
  { id: 'm-s3', displayName: '我的 R2', provider: 's3', accountId: 'a1', rootRef: '', rootLabel: 'test-bucket', endpointHost: 'abc123.r2.cloudflarestorage.com', group: '工作', caps: CAPS, status: 'ok', createdAt: '', updatedAt: '' },
  { id: 'm-s3aws', displayName: 'AWS 备份', provider: 's3', accountId: 'a4', rootRef: '', rootLabel: 'backup-bucket', endpointHost: 's3.ap-northeast-1.amazonaws.com', group: '存储 / 备份', caps: CAPS, status: 'ok', createdAt: '', updatedAt: '' },
  { id: 'm-db', displayName: 'Dropbox', provider: 'dropbox', accountId: 'a2', rootRef: '', rootLabel: '', group: '个人', caps: CAPS, status: 'ok', createdAt: '', updatedAt: '' },
  { id: 'm-dr', displayName: '工作 Drive', provider: 'drive', accountId: 'a3', rootRef: '', rootLabel: '', group: '工作', caps: { ...CAPS, idBased: true }, status: 'auth_expired', createdAt: '', updatedAt: '' },
  { id: 'm-od', displayName: 'OneDrive', provider: 'onedrive', accountId: 'a5', rootRef: '', rootLabel: '', group: '个人', caps: { ...CAPS, idBased: true }, status: 'ok', createdAt: '', updatedAt: '' },
  { id: 'm-drime', displayName: 'Drime', provider: 'drime', accountId: 'a6', rootRef: '', rootLabel: '', group: '个人', caps: CAPS, status: 'ok', createdAt: '', updatedAt: '' },
  { id: 'm-local', displayName: '本地照片', provider: 'local', accountId: 'a7', rootRef: '/Users/zy/照片', rootLabel: '/Users/zy/照片', group: '存储 / 备份', caps: CAPS, status: 'ok', createdAt: '', updatedAt: '' },
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
const STUB = `(() => {
  const empty = { 'DesktopService.ListMounts': ${STUB_MOUNTS}, 'DesktopService.ListAgents': [], 'DesktopService.Transfers': [], 'DesktopService.ListSyncJobs': [] }
  const listeners = {}
  window.mygo = {
    locale: 'zh-CN', dir: 'ltr',
    on: (name, fn) => { (listeners[name] ??= []).push(fn); return () => { listeners[name] = (listeners[name] ?? []).filter((f) => f !== fn) } },
    once: (name, fn) => { const wrap = (p) => { fn(p); listeners[name] = (listeners[name] ?? []).filter((f) => f !== wrap) }; (listeners[name] ??= []).push(wrap); return () => {} },
    call: async (method, ...args) => {
      if (method === 'DesktopService.AgentStatus') return { total: 0, online: 0 }
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
      if (method === 'DesktopService.GetSettings') return { theme: 'light', language: 'zh', downloadDir: '', syncCompare: 'size+time', syncConcurrency: 4, syncSpeedLimitKBps: 0, syncConflict: 'keep-both' }
      if (method === 'DesktopService.SaveSettings') return null
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
  await page.waitForTimeout(800)

  // 1. 文件浏览页：零网盘空态
  await page.screenshot({ path: path.join(SHOTS, '01-browse-empty.png') })

  // 1b. S3 向导回显（问题 1，零网盘模式）：选 S3、填桶 test-bucket +
  // 前缀 photos，第 3 步必须回显“将挂载：桶 test-bucket，范围：仅前
  // 缀 photos”，让用户确认后再测试保存。有网盘时没有空态添加按钮，
  // 这一步跳过（向导本身与模式无关）。
  if (!process.env.VISUAL_MOUNTS) {
    await page.getByRole('button', { name: '添加网盘', exact: true }).click()
    await page.waitForTimeout(400)
    await page.getByText('S3 / R2 / 兼容 S3').click()
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
    await page.getByRole('button', { name: '下一步：测试' }).click()
    await page.waitForTimeout(400)
    const recap = await page.locator('.dialog').textContent()
    console.log('s3 recap:', JSON.stringify(recap?.slice(0, 200)))
    if (!recap?.includes('将挂载：桶 test-bucket，范围：仅前缀 photos')) {
      failures.push('S3 向导第 3 步没有回显“将挂载：桶 test-bucket，范围：仅前缀 photos”')
    }
    await page.screenshot({ path: path.join(SHOTS, '01b-wizard-s3-recap.png') })
    await page.locator('.dialog-actions >> text=取消').click()
    await page.waitForTimeout(400)

    // 1c. Drime 向导（V0.6.0 起 PAT-only）：Drime 必须可选（不再
    // 标 V0.2），第 2 步只有两样——个人访问令牌（提示“令牌只
    // 显示一次”）和可选的工作区 ID；OAuth 相关字段（Client ID、
    // 生成授权链接、授权码、Refresh Token）必须一个都不出现。
    await page.getByRole('button', { name: '添加网盘', exact: true }).click()
    await page.waitForTimeout(400)
    const drimeCard = page.locator('.agent-row', { hasText: 'Drime' }).first()
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
    if (!drimeStep2?.includes('工作区 ID')) failures.push('Drime 向导第 2 步没有“工作区 ID”字段')
    await page.screenshot({ path: path.join(SHOTS, '01c-wizard-drime.png') })
    await page.locator('.dialog-actions >> text=取消').click()
    await page.waitForTimeout(400)

    // 1d. OneDrive 向导（V0.6.0）：OneDrive 必须可选（不再标
    // V0.2），第 2 步是 OAuth 授权码流程——有 Client ID、
    // “生成授权链接”、Azure 门户的建应用指引和 Refresh Token。
    await page.getByRole('button', { name: '添加网盘', exact: true }).click()
    await page.waitForTimeout(400)
    const odCard = page.locator('.agent-row', { hasText: 'OneDrive' }).first()
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

  // 3. 同步任务页：先列已保存任务（零任务空态）
  await page.locator('.nav-item', { hasText: '同步任务' }).click()
  await page.waitForTimeout(600)
  await page.screenshot({ path: path.join(SHOTS, '03-sync.png') })

  // 3b. 点“创建同步任务”：双栏编辑器应出现在顶部（方案 A）
  await page.getByRole('button', { name: '创建同步任务' }).click()
  await page.waitForTimeout(1200)
  await page.screenshot({ path: path.join(SHOTS, '03b-sync-create.png') })

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
  // 圆角 16px、节间距 16px），选择框统一样式（自定义箭头、
  // 固定高度 40px、圆角 8px）。
  const settingsCards = await page.evaluate(() => {
    const cards = [...document.querySelectorAll('.settings-card')]
    const selects = [...document.querySelectorAll('.settings-card select')]
    const cardInfo = cards.map((c) => {
      const cs = getComputedStyle(c)
      const r = c.getBoundingClientRect()
      return {
        bg: cs.backgroundColor,
        radius: cs.borderTopLeftRadius,
        marginBottom: cs.marginBottom,
        w: Math.round(r.width),
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
    return { cardCount: cards.length, cardInfo, selectCount: selects.length, selectInfo }
  })
  console.log('settings cards:', JSON.stringify(settingsCards))
  if (settingsCards.cardCount < 5) failures.push(`设置页分组卡片应至少 5 张（通用/网盘/同步默认/Agent/关于），实测 ${settingsCards.cardCount} 张`)
  for (const c of settingsCards.cardInfo) {
    if (c.bg !== 'rgb(245, 245, 245)') failures.push(`设置卡片背景应为 --card #F5F5F5，实测 ${c.bg}`)
    if (c.radius !== '16px') failures.push(`设置卡片圆角应为 16px，实测 ${c.radius}`)
    if (c.marginBottom !== '16px') failures.push(`设置卡片节间距应为 16px，实测 ${c.marginBottom}`)
  }
  if (settingsCards.selectCount === 0) failures.push('设置页里找不到选择框（.settings-card select）')
  for (const s of settingsCards.selectInfo) {
    if (s.h < 38 || s.h > 42) failures.push(`选择框高度应为 40px 左右，实测 ${s.h}px`)
    if (s.radius !== '8px') failures.push(`选择框圆角应为 8px，实测 ${s.radius}`)
    if (s.appearance !== 'none') failures.push(`选择框应去掉浏览器默认外观（appearance: none），实测 ${s.appearance}`)
    if (!s.hasArrow) failures.push('选择框没有自定义下拉箭头（background-image 应含 SVG）')
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
