#!/bin/sh
# setup-chromium.sh — 下一个 Playwright 版 Chromium 供 visual-check 用。
# 本机出站下载很慢（约 0.4MB/s），170MB 需要 7-8 分钟，请耐心等它跑完；
# 下好后解到 scripts/visual-check/.chromium/（已 gitignore，不进仓库）。
set -e
DIR="$(dirname "$0")/.chromium"
mkdir -p "$DIR"
cd /tmp
if [ ! -f chromium-linux.zip ]; then
  curl -sL -o chromium-linux.zip https://npmmirror.com/mirrors/playwright/builds/chromium/1200/chromium-linux.zip
fi
unzip -q -o chromium-linux.zip -d "$DIR"
ls "$DIR/chrome-linux64/chrome"
