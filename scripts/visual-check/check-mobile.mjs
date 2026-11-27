// check-mobile — mobile home screen check (phone viewports only).
//
// Usage (repo root):
//   node scripts/visual-check/check-mobile.mjs [shots-dir]
//   VISUAL_MOBILE_EMPTY=1 node scripts/visual-check/check-mobile.mjs
//
// Serves frontend/dist statically and opens ?mobile=1 in headless
// Chromium at 390×844 (iPhone 14) and 430×932 (large Android).
// Like check.mjs, it injects a window.mygo stub before load — the
// stub mounts/quotas below are CHECK-ONLY data (never in frontend
// code): they let the cards render with realistic numbers on this
// VM, where there are no user mounts. Real-device verification on
// the user's phone is still owed (see docs/mobile-app.md).
// VISUAL_MOBILE_EMPTY=1 returns zero mounts (empty state).
// The script also asserts the coarse-pointer path (hasTouch, no
// ?mobile) renders the mobile home, and that '/' without either
// still renders the desktop app — the main.tsx branch must not
// break desktop.
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
const SHOTS = path.resolve(process.argv[2] ?? path.join(path.dirname(fileURLToPath(import.meta.url)), 'shots-mobile'))
mkdirSync(SHOTS, { recursive: true })

const MIME = { '.html': 'text/html', '.js': 'text/javascript', '.mjs': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png' }

const CAPS = {
  canMkdir: true, canRename: true, canDelete: true, deleteToTrash: true,
  supportsServerSideCopy: false, supportsVersions: false, supportsShare: true,
  supportsSearch: true, supportsDelta: true, supportsThumbnail: false,
  supportsResumableUpload: true, supportsEncryption: false, supportsStorageClass: false,
  supportsPresignedUrl: false, idBased: false, maxFileSize: 0, minPartSize: 0, maxPathLen: 0, illegalChars: '',
}
const GB = 1024 ** 3
const TB = 1024 ** 4
const MB = 1024 ** 2
// Stub sync jobs / transfers / settings / cache (check-only): the
// same shapes the desktop pages consume, so the mobile views render
// with realistic data on this VM.
const MOCK_JOBS = [
  { id: 'j1', name: 'Drive → Dropbox 每日备份', srcMount: 'm-dr', srcRef: '/', dstMount: 'm-db', dstRef: '/backup', direction: 'copy', compare: 'size+time', schedule: '0 2 * * *', runOn: 'local', enabled: true, lastRunAt: '2026-11-26T02:00:00+08:00', lastRunOk: true, nextRunAt: '2026-11-27T02:00:00+08:00', createdAt: '', updatedAt: '' },
  { id: 'j2', name: '照片手动同步', srcMount: 'm-local', srcRef: '/Users/zy/照片', dstMount: 'm-dr', dstRef: '/照片', direction: 'copy', compare: 'size+time', schedule: '', runOn: 'local', enabled: false, lastRunAt: '', lastRunOk: false, nextRunAt: '', createdAt: '', updatedAt: '' },
]
const MOCK_TRANSFERS = [
  { id: 't1', kind: 'upload', srcMount: 'm-dr', srcRef: '/a.zip', dstMount: 'm-db', dstRef: '/a.zip', size: 100 * MB, done: 42 * MB, status: 'running', speedBps: 2 * MB, retries: 0 },
  { id: 't2', kind: 'download', srcMount: 'm-db', srcRef: '/b.pdf', dstMount: 'm-local', dstRef: '/Users/zy/下载/b.pdf', size: 5 * MB, done: 5 * MB, status: 'completed', speedBps: 0, retries: 0 },
  { id: 't3', kind: 'copy', srcMount: 'm-dr', srcRef: '/c.mov', dstMount: 'm-od', dstRef: '/c.mov', size: 50 * MB, done: 10 * MB, status: 'failed', speedBps: 0, retries: 1, error: '网络中断' },
]
const MOCK_SETTINGS = { theme: 'light', language: 'zh', downloadDir: '', syncCompare: 'size+time', syncConcurrency: 2, syncSpeedLimitKBps: 0, syncConflict: 'keep-both', proxyMode: 'system', proxyHttp: '', proxySocks5: '', previewCacheLimitMB: 2048, previewCacheClearOnExit: false }
const MOCK_CACHE = { sizeBytes: Math.round(1.2 * MB), fileCount: 3, limitMB: 2048 }
// Stub mounts (check-only): one per interesting quota state —
// normal (Drive 10/15 GB, Dropbox 17/30 GB), unlimited (R2),
// workspace-scoped (Drime 个人空间）, local disk, used-only
// total=-1 (Box), quota error (Dropbox "容量暂不可用").
const MOCK_MOUNTS = process.env.VISUAL_MOBILE_EMPTY
  ? []
  : [
      { id: 'm-dr', displayName: '我的 Drive', provider: 'drive', accountId: 'a1', rootRef: '', rootLabel: '', group: '', caps: { ...CAPS, idBased: true }, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
      { id: 'm-db', displayName: 'Dropbox', provider: 'dropbox', accountId: 'a2', rootRef: '', rootLabel: '', group: '', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
      { id: 'm-s3', displayName: '我的 R2', provider: 's3', accountId: 'a3', rootRef: '', rootLabel: 'test-bucket', endpointHost: 'abc123.r2.cloudflarestorage.com', group: '', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
      { id: 'm-drime', displayName: 'Drime', provider: 'drime', accountId: 'a4', rootRef: '', rootLabel: '', workspaceLabel: '个人空间', group: '', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
      { id: 'm-od', displayName: 'OneDrive', provider: 'onedrive', accountId: 'a5', rootRef: '', rootLabel: '', group: '', caps: { ...CAPS, idBased: true }, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
      { id: 'm-local', displayName: '本地照片', provider: 'local', accountId: 'a6', rootRef: '/Users/zy/照片', rootLabel: '/Users/zy/照片', group: '', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
      { id: 'm-box', displayName: 'Box', provider: 'box', accountId: 'a7', rootRef: '', rootLabel: '', group: '', caps: CAPS, status: 'ok', verified: true, createdAt: '', updatedAt: '' },
      { id: 'm-err', displayName: '旧 Dropbox', provider: 'dropbox', accountId: 'a8', rootRef: '', rootLabel: '', group: '', caps: CAPS, status: 'error', verified: true, createdAt: '', updatedAt: '' },
    ]
const STUB_QUOTAS = process.env.VISUAL_MOBILE_EMPTY
  ? []
  : [
      { mountId: 'm-dr', used: 10 * GB, total: 15 * GB, unlimited: false, isLocalDisk: false },
      { mountId: 'm-db', used: 17 * GB, total: 30 * GB, unlimited: false, isLocalDisk: false },
      { mountId: 'm-s3', used: 0, total: 0, unlimited: true, isLocalDisk: false },
      { mountId: 'm-drime', used: 291 * GB, total: 512 * GB, unlimited: false, isLocalDisk: false },
      { mountId: 'm-od', used: 236 * GB, total: 1 * TB, unlimited: false, isLocalDisk: false },
      { mountId: 'm-local', used: 400 * GB, total: 1 * TB, unlimited: false, isLocalDisk: true },
      { mountId: 'm-box', used: 5 * GB, total: -1, unlimited: false, isLocalDisk: false },
      { mountId: 'm-err', used: 0, total: 0, unlimited: false, isLocalDisk: false, error: '容量接口需要 account_info.read 权限' },
    ]

const STUB = `(() => {
  window.mygo = {
    locale: 'zh-CN', dir: 'ltr',
    on: () => () => {},
    once: () => () => {},
    call: async (method, ...args) => {
      if (method === 'DesktopService.ListMounts') return ${JSON.stringify(MOCK_MOUNTS)}
      if (method === 'DesktopService.MountQuotas') return ${JSON.stringify(STUB_QUOTAS)}
      if (method === 'DesktopService.ListSyncJobs') return ${JSON.stringify(MOCK_JOBS)}
      if (method === 'DesktopService.Transfers') return ${JSON.stringify(MOCK_TRANSFERS)}
      if (method === 'DesktopService.GetSettings') return ${JSON.stringify(MOCK_SETTINGS)}
      if (method === 'DesktopService.SaveSettings') return true
      if (method === 'DesktopService.PreviewCacheInfo') return ${JSON.stringify(MOCK_CACHE)}
      if (method === 'DesktopService.ClearPreviewCache') return { freedBytes: Math.round(1.2 * MB), fileCount: 3 }
      if (method === 'DesktopService.RetryTransfer') return true
      throw new Error('stub: unimplemented ' + method)
    },
    callWith: async (method, args) => window.mygo.call(method, ...(args ?? [])),
  }
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
const check = (cond, msg) => {
  if (!cond) failures.push(msg)
  else console.log('ok:', msg)
}

try {
  // 1. 390×844 (?mobile=1): the home screen.
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  page.on('pageerror', (e) => failures.push(`pageerror: ${e.message}`))
  await page.addInitScript(STUB)
  await page.goto(`http://127.0.0.1:${port}/?mobile=1`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(800)

  const home = await page.evaluate(() => {
    const cards = [...document.querySelectorAll('.mhome-card[data-mount-id]')]
    const card = (id) => cards.find((c) => c.dataset.mountId === id)
    const ringPct = (id) => card(id)?.querySelector('.mhome-ring-pct')?.textContent ?? null
    const ringStroke = (id) => {
      const circles = card(id)?.querySelectorAll('.mhome-ring circle')
      return circles?.[1]?.getAttribute('stroke') ?? null
    }
    const ringTrack = (id) => {
      const circles = card(id)?.querySelectorAll('.mhome-ring circle')
      return circles?.[0]?.getAttribute('stroke') ?? null
    }
    const driveCard = card('m-dr')
    const driveName = driveCard?.querySelector('.mhome-name')
    const driveRingEl = driveCard?.querySelector('.mhome-ring')
    const carousel = document.querySelector('.mhome-carousel')
    const cs = carousel ? getComputedStyle(carousel) : null
    const header = document.querySelector('.mhome-header')
    const headerTitle = document.querySelector('.mhome-title')
    const nav = document.querySelector('.mhome-nav')
    return {
      title: document.querySelector('.mhome-title')?.textContent ?? null,
      subtitle: document.querySelector('.mhome-subtitle')?.textContent ?? null,
      headerTitleAlign: headerTitle ? getComputedStyle(headerTitle).textAlign : null,
      headerTitleWeight: headerTitle ? getComputedStyle(headerTitle).fontWeight : null,
      headerBg: header ? getComputedStyle(header).backgroundColor : null,
      hasBell: [...document.querySelectorAll('button')].some((b) => (b.getAttribute('aria-label') ?? '').includes('通知')) || document.body.innerText.includes('通知'),
      hasHeaderRefreshBtn: !!document.querySelector('.mhome-refresh-btn'),
      navLabels: [...(nav?.querySelectorAll('.mhome-nav-item') ?? [])].map((b) => b.textContent?.trim()),
      navHasDigits: /\d/.test(nav?.innerText ?? ''),
      cardCount: cards.length,
      cardIds: cards.map((c) => c.dataset.mountId),
      driveText: card('m-dr')?.innerText ?? null,
      drivePct: ringPct('m-dr'),
      driveRing: ringStroke('m-dr'),
      driveRingTrack: ringTrack('m-dr'),
      driveCardBg: driveCard ? getComputedStyle(driveCard).backgroundColor : null,
      driveCardHeight: driveCard ? Math.round(driveCard.getBoundingClientRect().height) : null,
      driveNameWeight: driveName ? getComputedStyle(driveName).fontWeight : null,
      driveRingSize: driveRingEl ? driveRingEl.getBoundingClientRect().width : null,
      dropboxPct: ringPct('m-db'),
      s3Text: card('m-s3')?.innerText ?? null,
      s3Pct: ringPct('m-s3'),
      errText: card('m-err')?.innerText ?? null,
      localText: card('m-local')?.innerText ?? null,
      boxText: card('m-box')?.innerText ?? null,
      drimeText: card('m-drime')?.innerText ?? null,
      carouselOverflow: carousel ? carousel.scrollWidth > carousel.clientWidth : false,
      snapType: cs ? cs.scrollSnapType : null,
      hasAddMount: [...document.querySelectorAll('button, a')].some((el) => el.textContent?.includes('添加网盘')),
      emptyTitle: document.querySelector('.mhome-empty-title')?.textContent ?? null,
    }
  })
  console.log('home 390:', JSON.stringify(home, null, 1))
  if (!process.env.VISUAL_MOBILE_EMPTY) {
    await page.screenshot({ path: path.join(SHOTS, 'm-390-home.png') })
  }

  if (process.env.VISUAL_MOBILE_EMPTY) {
    await page.screenshot({ path: path.join(SHOTS, 'm-390-empty.png') })
    check(home.title === '我的网盘', `标题应为“我的网盘”，实测 ${home.title}`)
    check(home.emptyTitle === '还没有网盘', `空态标题应为“还没有网盘”，实测 ${home.emptyTitle}`)
    check(home.cardCount === 0, `空态不应有卡片，实测 ${home.cardCount} 张`)
    check(!home.hasAddMount, '空态也不应出现“添加网盘”入口（手机端不能加网盘）')
    check(!home.hasBell, '空态也不应有铃铛/通知入口')
    check(JSON.stringify(home.navLabels) === JSON.stringify(['网盘', '同步', '传输', '设置']), `空态底部导航也应为 网盘/同步/传输/设置，实测 ${JSON.stringify(home.navLabels)}`)
  } else {
    check(home.title === '我的网盘', `标题应为“我的网盘”，实测 ${home.title}`)
    check(!!home.subtitle, '副标题应存在')
    check(home.headerTitleAlign === 'center', `标题应居中，实测 ${home.headerTitleAlign}`)
    check(home.headerTitleWeight === '700', `标题应加粗（700），实测 ${home.headerTitleWeight}`)
    check(home.headerBg === 'rgba(86, 225, 108, 0.22)', `顶栏底应为品牌绿 22%（更深的薄荷绿），实测 ${home.headerBg}`)
    check(!home.hasBell, '不应有铃铛/通知入口（无通知数据源）')
    check(!home.hasHeaderRefreshBtn, '顶栏不应再有刷新按钮（刷新已移到 ··· 菜单）')
    check(JSON.stringify(home.navLabels) === JSON.stringify(['网盘', '同步', '传输', '设置']), `底部导航应为 网盘/同步/传输/设置，实测 ${JSON.stringify(home.navLabels)}`)
    check(!home.navHasDigits, '底部导航不应带任何数字角标（没有跨网盘索引可计数）')
    check(home.cardCount === 8, `应有 8 张存储卡片，实测 ${home.cardCount} 张 (${home.cardIds.join(',')})`)
    check(!!home.driveText?.includes('10/15 GB'), `Drive 卡应显示紧凑“10/15 GB”，实测 ${JSON.stringify(home.driveText)}`)
    check(home.drivePct === '67%', `Drive 环应为 67%，实测 ${home.drivePct}`)
    check(home.driveRing === '#F5B81C', `Drive 环应用主题琥珀黄 #F5B81C（稿子色，非品牌蓝），实测 ${home.driveRing}`)
    check(home.driveRingTrack === '#ffffff', `圆环底轨应为白色，实测 ${home.driveRingTrack}`)
    check(home.driveCardBg === 'rgba(245, 184, 28, 0.1)', `Drive 卡底应为琥珀黄 10%（暖米色），实测 ${home.driveCardBg}`)
    check((home.driveCardHeight ?? 0) >= 320, `卡片应为竖长卡（高 ≥ 320px），实测 ${home.driveCardHeight}px`)
    check(home.driveNameWeight === '700', `卡片名字应加粗（700），实测 ${home.driveNameWeight}`)
    check(home.driveRingSize === 88, `圆环直径应为 88px，实测 ${home.driveRingSize}`)
    check(home.dropboxPct === '57%', `Dropbox 环应为 57%，实测 ${home.dropboxPct}`)
    check(!!home.s3Text?.includes('不限量'), `R2 卡应显示“不限量”，实测 ${JSON.stringify(home.s3Text)}`)
    check(home.s3Pct === '∞', `R2 卡环位应为 ∞（不限量、非百分比），实测 ${home.s3Pct}`)
    check(!!home.errText?.includes('容量暂不可用'), `故障卡应显示“容量暂不可用”，实测 ${JSON.stringify(home.errText)}`)
    check(!!home.localText?.includes('本地磁盘'), `本地卡应标“本地磁盘”，实测 ${JSON.stringify(home.localText)}`)
    check(!!home.boxText?.includes('已用 5 GB'), `Box 卡（仅已用量、无总量）应显示紧凑“已用 5 GB”，实测 ${JSON.stringify(home.boxText)}`)
    check(!!home.drimeText?.includes('个人空间'), `Drime 卡副标题应带工作区“个人空间”，实测 ${JSON.stringify(home.drimeText)}`)
    check(home.carouselOverflow, '多卡时轮播应可横向滚动（下一张露出一点）')
    check(!!home.snapType?.includes('mandatory'), `轮播应为 scroll-snap mandatory，实测 ${home.snapType}`)
    check(!home.hasAddMount, '首页不应出现“添加网盘”入口（手机端不能加网盘）')

    // 2. 卡片右上角 ··· 菜单：弹出底部动作条（刷新容量），取消关闭。
    await page.locator('.mhome-card[data-mount-id="m-dr"] .mhome-menu-btn').click()
    await page.waitForTimeout(300)
    const sheet = await page.evaluate(() => ({
      visible: !!document.querySelector('.mhome-sheet'),
      hasRefresh: document.querySelector('.mhome-sheet')?.innerText.includes('刷新容量') ?? false,
    }))
    check(sheet.visible && sheet.hasRefresh, `··· 菜单应弹出含“刷新容量”的动作条，实测 ${JSON.stringify(sheet)}`)
    await page.screenshot({ path: path.join(SHOTS, 'm-390-sheet.png') })
    await page.getByRole('button', { name: '取消', exact: true }).click()
    await page.waitForTimeout(200)
    check((await page.locator('.mhome-sheet').count()) === 0, '点取消后动作条应关闭')

    // 2b. 筛选药丸：云盘 7 张 / 本地 1 张 / 全部 8 张。
    const cardCount = () => page.locator('.mhome-card[data-mount-id]').count()
    await page.getByRole('button', { name: '云盘', exact: true }).click()
    await page.waitForTimeout(200)
    check((await cardCount()) === 7, `筛“云盘”应剩 7 张（除本地外全是云盘），实测 ${await cardCount()} 张`)
    await page.getByRole('button', { name: '本地', exact: true }).click()
    await page.waitForTimeout(200)
    check((await cardCount()) === 1, `筛“本地”应剩 1 张，实测 ${await cardCount()} 张`)
    await page.getByRole('button', { name: '全部', exact: true }).click()
    await page.waitForTimeout(200)
    check((await cardCount()) === 8, `筛“全部”应回 8 张，实测 ${await cardCount()} 张`)

    // 2c. 搜索：按网盘名即时过滤；无匹配给人话提示，不编结果。
    await page.getByRole('button', { name: '搜索网盘', exact: true }).click()
    await page.waitForTimeout(200)
    check((await page.locator('.mhome-search-row input').count()) === 1, '点搜索图标应展开搜索框')
    await page.locator('.mhome-search-row input').fill('Drive')
    await page.waitForTimeout(200)
    check((await cardCount()) === 2, `搜“Drive”应剩 2 张（我的 Drive + OneDrive，子串匹配），实测 ${await cardCount()} 张`)
    await page.locator('.mhome-search-row input').fill('R2')
    await page.waitForTimeout(200)
    check((await cardCount()) === 1, `搜“R2”应剩 1 张（我的 R2），实测 ${await cardCount()} 张`)
    await page.locator('.mhome-search-row input').fill('zzz')
    await page.waitForTimeout(200)
    check((await page.locator('.mhome-no-match').count()) === 1, '搜不到时应显示“没有匹配的网盘”')
    check(((await page.locator('.mhome-no-match').innerText()).includes('没有匹配的网盘')), '无匹配文案应为“没有匹配的网盘”')
    await page.locator('.mhome-search-row input').fill('')
    await page.waitForTimeout(200)
    check((await cardCount()) === 8, `清空搜索应回 8 张，实测 ${await cardCount()} 张`)

    // 2d. 查看全部：轮播 ↔ 竖直列表切换；列表模式下提示字隐藏。
    await page.getByRole('button', { name: '查看全部', exact: true }).click()
    await page.waitForTimeout(200)
    check((await page.locator('.mhome-list').count()) === 1, '点“查看全部”应切到竖直列表')
    check((await page.locator('.mhome-carousel').count()) === 0, '列表模式下不应再有横向轮播')
    check((await page.locator('.mhome-hint').count()) === 0, '列表模式下“左右滑动”提示应隐藏')
    check((await page.getByRole('button', { name: '收起', exact: true }).count()) === 1, '列表模式下按钮应读“收起”')
    await page.getByRole('button', { name: '收起', exact: true }).click()
    await page.waitForTimeout(200)
    check((await page.locator('.mhome-carousel').count()) === 1, '点“收起”应回轮播')
    check((await page.locator('.mhome-hint').count()) === 1, '回轮播后提示字应恢复')

    // 2e. 顶栏 ···（grid）菜单：刷新全部容量 + 查看全部（切列表）。
    await page.getByRole('button', { name: '更多', exact: true }).click()
    await page.waitForTimeout(300)
    const gridSheet = await page.evaluate(() => document.querySelector('.mhome-sheet')?.innerText ?? '')
    check(gridSheet.includes('刷新全部容量') && gridSheet.includes('查看全部'), `grid 菜单应含“刷新全部容量”与“查看全部”，实测 ${JSON.stringify(gridSheet)}`)
    await page.locator('.mhome-sheet').getByRole('button', { name: '查看全部', exact: true }).click()
    await page.waitForTimeout(200)
    check((await page.locator('.mhome-list').count()) === 1, '菜单里的“查看全部”也应切到列表')
    await page.getByRole('button', { name: '收起', exact: true }).click()
    await page.waitForTimeout(200)

    // 2f. 底部导航：同步 / 传输 / 设置三个视图都真实可用，再回网盘。
    await page.locator('.mhome-nav').getByRole('button', { name: '同步', exact: true }).click()
    await page.waitForTimeout(500)
    const syncText = await page.evaluate(() => document.querySelector('.mhome')?.innerText ?? '')
    check(syncText.includes('Drive → Dropbox 每日备份') && syncText.includes('照片手动同步'), '同步视图应列出两个任务名')
    check(syncText.includes('已启用') && syncText.includes('已停用'), '同步视图应显示启用状态')
    await page.locator('.mhome-nav').getByRole('button', { name: '传输', exact: true }).click()
    await page.waitForTimeout(500)
    const trText = await page.evaluate(() => document.querySelector('.mhome')?.innerText ?? '')
    check(trText.includes('a.zip') && trText.includes('b.pdf') && trText.includes('c.mov'), '传输视图应列出三条传输')
    check(trText.includes('进行中') && trText.includes('已完成') && trText.includes('失败'), '传输视图应显示三种状态')
    check((await page.getByRole('button', { name: '重试', exact: true }).count()) === 1, '失败的传输应有“重试”按钮')
    await page.getByRole('button', { name: '重试', exact: true }).click()
    await page.waitForTimeout(300)
    await page.locator('.mhome-nav').getByRole('button', { name: '设置', exact: true }).click()
    await page.waitForTimeout(500)
    const setText = await page.evaluate(() => document.querySelector('.mhome')?.innerText ?? '')
    check(setText.includes('缓存') && setText.includes('1.2 MB') && setText.includes('3 个文件'), `设置视图应有缓存卡（1.2 MB / 3 个文件），实测 ${JSON.stringify(setText.slice(0, 200))}`)
    await page.getByRole('button', { name: '深色', exact: true }).click()
    await page.waitForTimeout(200)
    check((await page.getByRole('button', { name: '深色', exact: true }).getAttribute('aria-pressed')) === 'true', '点“深色”后外观应切到深色（aria-pressed）')
    await page.screenshot({ path: path.join(SHOTS, 'm-390-settings.png') })
    await page.locator('.mhome-nav').getByRole('button', { name: '网盘', exact: true }).click()
    await page.waitForTimeout(500)
    check((await page.locator('.mhome-card[data-mount-id]').count()) === 8, '回“网盘”后应重新显示 8 张卡片')
  }

  // 3. 430×932 (?mobile=1): large Android width.
  const page2 = await browser.newPage({ viewport: { width: 430, height: 932 } })
  page2.on('pageerror', (e) => failures.push(`pageerror 430: ${e.message}`))
  await page2.addInitScript(STUB)
  await page2.goto(`http://127.0.0.1:${port}/?mobile=1`, { waitUntil: 'networkidle' })
  await page2.waitForTimeout(800)
  const wide = await page2.evaluate(() => ({
    title: document.querySelector('.mhome-title')?.textContent ?? null,
    cards: document.querySelectorAll('.mhome-card[data-mount-id]').length,
    overflowX: document.documentElement.scrollWidth > document.documentElement.clientWidth,
    navItems: document.querySelectorAll('.mhome-nav-item').length,
  }))
  console.log('home 430:', JSON.stringify(wide))
  check(wide.title === '我的网盘', `430 宽标题应为“我的网盘”，实测 ${wide.title}`)
  if (!process.env.VISUAL_MOBILE_EMPTY) check(wide.cards === 8, `430 宽应有 8 张卡片，实测 ${wide.cards} 张`)
  check(!wide.overflowX, '页面不应横向溢出（只有轮播内部可横滑）')
  check(wide.navItems === 4, `430 宽底部导航应有 4 项，实测 ${wide.navItems} 项`)
  if (!process.env.VISUAL_MOBILE_EMPTY) {
    await page2.screenshot({ path: path.join(SHOTS, 'm-430-home.png') })
  }

  // 4. 粗指针路径（hasTouch、无 ?mobile）：真机 webview 就靠它识别。
  const page3 = await browser.newPage({ viewport: { width: 390, height: 844 }, hasTouch: true })
  page3.on('pageerror', (e) => failures.push(`pageerror touch: ${e.message}`))
  await page3.addInitScript(STUB)
  await page3.goto(`http://127.0.0.1:${port}/`, { waitUntil: 'networkidle' })
  await page3.waitForTimeout(800)
  const touchTitle = await page3.evaluate(() => document.querySelector('.mhome-title')?.textContent ?? null)
  check(touchTitle === '我的网盘', `粗指针（触屏）无参数也应进手机首页，实测 ${touchTitle}`)

  // 5. 桌面路径不受影响：无参数 + 细指针仍进桌面 App。
  const page4 = await browser.newPage({ viewport: { width: 1280, height: 800 } })
  page4.on('pageerror', (e) => failures.push(`pageerror desktop: ${e.message}`))
  await page4.addInitScript(STUB)
  await page4.goto(`http://127.0.0.1:${port}/`, { waitUntil: 'networkidle' })
  await page4.waitForTimeout(800)
  const desktop = await page4.evaluate(() => ({
    logo: !!document.querySelector('img.app-logo'),
    mobileHome: !!document.querySelector('.mhome'),
  }))
  check(desktop.logo && !desktop.mobileHome, `桌面路径应仍渲染桌面 App（logo ${desktop.logo}，手机首页 ${desktop.mobileHome}）`)
} finally {
  await browser.close()
  server.close()
}

if (failures.length) {
  console.error(`\ncheck-mobile: ${failures.length} 个断言失败:`)
  for (const f of failures) console.error(' -', f)
  process.exitCode = 1
} else {
  console.log('\ncheck-mobile: 全部断言通过')
}
