// Export the hand-drawn logo.svg to PNGs with headless Chromium
// (no rsvg-convert / ImageMagick / cairosvg on this machine).
// The SVG markup is inlined into the page (a file:// <img> from an
// about:blank page does not load reliably), then screenshotted with a
// transparent background.
//   - frontend/public/logo.png: the logo at native aspect, 1024x723 (user-supplied artwork, 398:281, viewBox cropped per user).
//   - resources/icon.png: the mygo app icon — 1024x1024 square,
//     logo centred with padding (880x622), transparent background.
// Both are rendered from frontend/public/logo.svg, never from the
// webp reference image.
import { chromium } from 'playwright-core'
import { readFileSync, existsSync } from 'node:fs'
import path from 'node:path'

const ROOT = path.resolve(path.dirname(new URL(import.meta.url).pathname), '..', '..')
const CHROMIUM = existsSync(path.join(ROOT, 'scripts/visual-check/.chromium/chrome-linux64/chrome'))
  ? path.join(ROOT, 'scripts/visual-check/.chromium/chrome-linux64/chrome')
  : '/opt/meta-chromium/chrome'

const svg = readFileSync(path.join(ROOT, 'frontend/public/logo.svg'), 'utf8')
const browser = await chromium.launch({ executablePath: CHROMIUM, args: ['--no-sandbox', '--force-color-profile=srgb'] })
const page = await browser.newPage({ viewport: { width: 1024, height: 723 } })

// 1. logo.png — native 398:281 aspect at 1024 wide.
await page.setViewportSize({ width: 1024, height: 723 })
await page.setContent(`<body style="margin:0;background:transparent">${svg.replace('<svg ', '<svg style="width:1024px;height:723px;display:block" ')}</body>`)
await page.waitForTimeout(300)
await page.screenshot({ path: path.join(ROOT, 'frontend/public/logo.png'), omitBackground: true })

// 2. icon.png — 1024x1024, logo centred with ~7% padding each side.
await page.setViewportSize({ width: 1024, height: 723 })
await page.setContent(`<body style="margin:0;background:transparent;display:flex;align-items:center;justify-content:center;height:100vh">${svg.replace('<svg ', '<svg style="width:880px;height:622px;display:block" ')}</body>`)
await page.waitForTimeout(300)
await page.screenshot({ path: path.join(ROOT, 'resources/icon.png'), omitBackground: true })

await browser.close()
console.log('wrote frontend/public/logo.png + resources/icon.png')
