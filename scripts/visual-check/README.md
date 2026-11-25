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

- `01-browse-empty.png`：文件浏览页零网盘空态；脚本还断言顶栏品牌 logo：`img.app-logo`（`frontend/public/logo.svg`）在 ZyDrive 文字旁、26px 高、加载成功，favicon（`link[rel=icon]`）也指向 `/logo.svg`
- `01a-wizard-providers.png`：添加网盘第 1 步（原型 Demo v8 大图标网格）：两列卡片，每张大品牌 logo + 名字 + 一行小字，点整张卡选中（绿边框 + 淡绿底）；脚本断言 6 张卡、两列、都有 logo、点 S3 卡片后有 .selected 且边框为品牌绿
- `01b-wizard-s3-form.png`：S3 一页表单（C）：同一页上有“测试连接”和“保存”，没有第 3 步、没有“下一步：测试”；点测试连接显示“连接测试通过”，点保存后向导关闭、侧栏出现新网盘
- `01e-wizard-dropbox.png` / `01f-wizard-drive.png`：Dropbox 与 Drive 第 2 步字段/文案必须按网盘区分（Dropbox 是 App key + Dropbox App Console + files.metadata.read；Drive 是 OAuth 客户端 ID + Google Cloud Console + Drive scope），互相不许串对方的字段名和控制台名
- `01g-wizard-oneclick.png`：Dropbox 一键授权（A+B，桩配了内置 Client ID）：第 2 步有一键授权卡片、自建应用收进“高级”折叠；点“打开浏览器授权”后，桩的 PollOAuthCallback 立刻返回 done，Access/Refresh Token 自动填进表单并显示“Token 已换取成功”，手动粘贴 code 的兜底框仍在
- `02-sidebar-menu.png`：点侧栏网盘标题旁的 +，浮层菜单只有“新建网盘 / 新建分组”（300ms 基础曲线淡入上移）；脚本同时断言侧栏里没有“+ 新建分组”大按钮和全宽“+ 添加网盘”按钮
- `02f-sidebar-quota.png`：侧栏容量条——Drime 显示“已用 291.0 GB / 512.0 GB”（compact 版不带“共”字）+ 进度条，S3/R2 显示“不限量”，本地文件夹注明“本机磁盘”（单独一行），查询失败的那块显示“容量暂不可用”；未验证的网盘（本地照片桩）有“未验证”徽章
- `02g-sidebar-long-name.png`：超长网盘名（30 个汉字）悬停删除按钮时仍是单行省略、不换行；删除按钮为绝对定位（不占布局），名字有 title 全名
- `02-new-group-open.png` + `02b-sidebar-new-group.png`（侧栏局部放大）：
  从 + 菜单点“新建分组”后，输入框独占一行，“确定/取消”在下一行；
  脚本还会量按钮尺寸并断言：按钮宽必须大于高（横排），且不溢出侧栏
- `02d-upload-target.png`（仅 `VISUAL_MOUNTS=1`）：模拟一次拖放后先弹上传目标选择框（默认当前文件夹、显示完整路径“目标：网盘名 …”、有“上传到这里”和“新建文件夹”），而不是立即上传
- `03-sync.png`：同步任务页（先列已保存任务）
- `03c-sync-select.png` / `03d-sync-enter-folder.png`（仅 `VISUAL_MOUNTS=1`）：双栏点选——单击一行（文件或文件夹）即选中，选中行有绿底 + 左绿边 + 勾选标记，两栏各自独立，双栏上方回显的源/目标路径就是选中的路径；双击文件夹进入后根目录变为该文件夹
- `04-settings.png`：设置页（Agent 列表）
- 设置页分区断言：每节一张卡（背景 --card、圆角 16px、节间距 24px、顶部 3px 品牌绿条），节标题 18px/字重 400 + flat-color-icons 小图标、标题下有分割线；“高级”折叠至少 2 处（通用开机启动、同步默认并发与限速）
- `04d-edit-root.png`（仅 `VISUAL_MOUNTS=1`）：设置页 S3 网盘行的“修改桶 / 前缀”对话框，预填当前桶名
- `04e-settings-retest.png`（仅 `VISUAL_MOUNTS=1`）：设置页未验证网盘行的“重新测试”——点后显示“测试通过”，“未验证”徽章消失
- `04-mid-transition.png`：点进设置页 150ms 时的动效中间态——页面过渡（500ms 基础曲线）必须还在跑，脚本会量 `.page-enter` 的 opacity（须在 0~1 之间），瞬间硬切会在这里失败
- `04c-settings-dark.png`：深色主题下的设置页（脚本会断言 body 底色变深）
- 动效配置审计：主按钮的 transition 曲线必须全是基础曲线 `cubic-bezier(0.22, 1, 0.36, 1)`，页面过渡 500ms，hover 颜色 300ms；绝不出现浏览器默认 ease/linear
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
