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
    return {
      title: document.querySelector('.mhome-title')?.textContent ?? null,
      subtitle: document.querySelector('.mhome-subtitle')?.textContent ?? null,
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
  } else {
    check(home.title === '我的网盘', `标题应为“我的网盘”，实测 ${home.title}`)
    check(!!home.subtitle, '副标题应存在')
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
  }))
  console.log('home 430:', JSON.stringify(wide))
  check(wide.title === '我的网盘', `430 宽标题应为“我的网盘”，实测 ${wide.title}`)
  if (!process.env.VISUAL_MOBILE_EMPTY) check(wide.cards === 8, `430 宽应有 8 张卡片，实测 ${wide.cards} 张`)
  check(!wide.overflowX, '页面不应横向溢出（只有轮播内部可横滑）')
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
