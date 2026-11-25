# 截图自查（visual-check）

写完前端代码后、推送之前，先截图自己看一遍。用户 2026-11-25 的要求：
Mac 真窗口上侧栏“新建分组”的“确定”按钮竖排了——这种布局问题应该在
推送前就被截图检查拦下来，而不是等用户在真机上发现。

## 怎么跑

```sh
# 先构建前端（检查跑的是生产构建产物 frontend/dist）
npm --prefix frontend run build
# 再截图
node scripts/visual-check/check.mjs
# 截图在 scripts/visual-check/shots/（已 gitignore，不推送）
```

## 检查什么

- `01-browse-empty.png`：文件浏览页零网盘空态
- `02-new-group-open.png` + `02b-sidebar-new-group.png`（侧栏局部放大）：
  点开“+ 新建分组”后，输入框独占一行，“确定/取消”在下一行；
  脚本还会量按钮尺寸并断言：按钮宽必须大于高（横排），且不溢出侧栏
- `03-sync.png`：同步任务页（先列已保存任务）
- `04-settings.png`：设置页（Agent 列表）
- `05-browse-no-stub.png`：不注桩时 Bind 失败的人话错误态
- 脚本最后检查整页有无横向溢出（scrollWidth > clientWidth 即失败）

## 原理

纯浏览器里没有 mygo 运行时，脚本在页面加载前注入一个测试专用的
`window.mygo` 桩：所有 Bind 调用返回空数据（零网盘、零 Agent、零任务、
零传输），只看布局不看数据。桩只活在检查脚本里，不进前端代码。

Chromium 用 `scripts/visual-check/.chromium/chrome-linux64/chrome`（首次跑
`node scripts/visual-check/check.mjs` 前，先从 npmmirror 下一个 Playwright
版 Chromium 解到该位置：见 `scripts/visual-check/setup-chromium.sh`；已
gitignore，不进仓库）。如果本机有 `/opt/meta-chromium/chrome` 且能直接
启动，也会作为后备。注意：/opt/meta-chromium 是 Muse 托管浏览器的二
进制，带启动策略限制，在本机直接启动会报错，不要指望它。
