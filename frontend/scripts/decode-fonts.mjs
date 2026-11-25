// decode-fonts — 把 base64 存的字体文件解回 public/fonts/*.woff2。
//
// 为什么字体要 base64 存在仓库里：GitHub 快照推送（snapshot push）
// 只接受 UTF-8 文本，woff2 二进制推不上去；所以仓库里只存
// frontend/fonts-src/*.woff2.b64 文本，构建前由 npm 的 prebuild
// 自动解码成 public/fonts/*.woff2（离线可用，不依赖 Google Fonts
// 在线链接）。解出的 .woff2 是构建产物，已 gitignore，不要手工提交。
import { readdirSync, readFileSync, writeFileSync, existsSync, mkdirSync } from 'node:fs'
import { dirname, join, basename } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const srcDir = join(root, 'fonts-src')
const outDir = join(root, 'public', 'fonts')
mkdirSync(outDir, { recursive: true })
for (const f of readdirSync(srcDir)) {
  if (!f.endsWith('.woff2.b64')) continue
  const out = join(outDir, basename(f, '.b64'))
  const b64 = readFileSync(join(srcDir, f), 'utf8').trim()
  const buf = Buffer.from(b64, 'base64')
  if (existsSync(out) && readFileSync(out).equals(buf)) continue
  writeFileSync(out, buf)
  console.log(`decode-fonts: ${f} -> public/fonts/${basename(out)} (${buf.length} bytes)`)
}
