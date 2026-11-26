# ZyDrive 手机 App（进行中：首页存储卡片）

> 状态（2026-11-26）：首页存储卡片已完成、本地验证通过，**未推送**。
> 没有手机二进制被构建出来过（本 VM 无 Android SDK、不是 Mac），
> 真机验证全部待用户在手机上做。

## 一、已决定的手机端范围（用户 2026-11-26 拍板）

手机端只做三件事：**查看/浏览、下载、上传**。**手机上不加网盘**——
没有添加网盘流程，首页、空态、任何地方都没有“添加网盘”入口
（检查脚本会断言这一点）。

**网盘怎么到手机上（已决定，未实现）：走桌面端的加密配置导出/导入。**
桌面导出加密配置包，用户传到手机上导入，手机从此拥有同样的
网盘与凭证。不在手机 App 里做 OAuth 添加流程。待定的子问题：
传输方式（文件分享 / AirDrop / 二维码传包三选一）、首次启动
导入的引导文案、手机端是否沿用同一套 master.key 体系（建议是：
同一套，凭证在手机本地解密，不经过我们的服务器）。

## 二、首页做了什么

设计参照：Adom Shafi 的 Dribbble 稿子 “Cloud Storage App”
（原图在 `~/workspace/browser_downloads/sess-1810883329/`）。
只取了卡片的**版式**，图是按我们自己的色板重画的：

- 顶部：浅绿底圆角 header（品牌绿 8% 透明度），小字 “管理” +
  大标题 “我的网盘” + 一行副标题，右上角是真的刷新按钮。
- 存储卡片（横向 snap-scroll 轮播，一张整卡 + 下一张露出一点，
  和稿子一样；窄屏手机上列表式排布不如轮播省地方，故选轮播）：
  品牌 logo 在白圆里（复用仓库已有的 Simple Icons 品牌 logo，
  不重新下载）、右上角 ··· 菜单、网盘名、副标题（品牌名 + Drime
  工作区 / S3 桶名——Mount 没有 email 字段，accountId 是密钥引用
  不能展示，所以副标题不是邮箱）、左下 “已用 / 总量” + “剩余 X”、
  右下粗圆环 + 中央百分比。
- 卡片底色是各家品牌色 10% 透明度、圆环是品牌色全色，色值取自
  仓库 `icons.tsx` 自己的品牌表（Drive #4285F4、Dropbox #0061FF、
  OneDrive #0078D4、Drime #56E16C、S3 #569A31、R2 #F38020、Box
  #0061D5、MEGA #D9272E、pCloud #17BED0），不是稿子的原色值。
- 字重全站 400 不变（稿子名字和百分比是粗体，这是本版有意与
  稿子的偏差；用户一句话可豁免手机首页）。

数据全部走真实后端接口（桌面侧栏同一批调用）：
`api.listMounts()` + `api.mountQuotas()`，没有第二套后端。

- S3/R2 没有配额接口：后端报 Unlimited，卡片写“不限量”，
  环位放 ∞（不是百分比，绝不编数字）。
- 只有已用量、没有总量（total = -1）：写“已用 X”+“不限量”，
  环位同样是 ∞。
- 本地磁盘：环正常算，小字注明“本地磁盘 · 剩余 X”。
- 容量查不到：卡片显示“容量暂不可用”+ 后端给的人话原因。
- 零网盘空态：“还没有网盘。手机端不能添加网盘，请先在桌面端
  添加网盘。”（不承诺尚不存在的导入流程）。
- 加载中只显示骨架卡片；后端连不上显示人话错误 + 重试。

··· 菜单目前只有一个真动作：刷新容量（进底部动作条）。
卡片本身本版点按无动作——点卡片应打开该网盘的文件列表，
但手机浏览视图还没做；没有去向的卡片不装成按钮。

### 文件清单

| 文件 | 说明 |
| --- | --- |
| `frontend/src/mobile/MobileHome.tsx` | 首页：header、轮播、存储卡、圆环、··· 动作条、空态/骨架/错误态 |
| `frontend/src/mobile/mobile.css` | 手机样式，全部包在 `.mhome` 作用域内，不污染桌面 |
| `frontend/src/main.tsx`（改） | 入口分流：`?mobile=1` 或粗指针（触屏）时渲染手机首页，否则桌面 App |
| `scripts/visual-check/check-mobile.mjs` | 手机检查脚本（新文件；不改 `check.mjs`） |
| `docs/mobile-app.md` | 本文件 |

没有碰版本文件（v0.15.0 桌面工作在并行，版本文件归它管），
没有改任何桌面组件。

### 入口识别

手机首页靠运行时判断：`?mobile=1` 查询参数，或
`matchMedia('(pointer: coarse)')`。真机 webview 应报粗指针，
因此手机包无需改 `mygo.json`、不用单独入口。桌面预览：
`npm run dev --prefix frontend` 后打开
`http://localhost:5173/?mobile=1`。

## 三、mygo 手机构建矩阵（2026-11-26 在本 VM 上实测）

mygo 0.10.0，PR #940（2026-10-31）合入的手机支持。

| 目标 | 本 VM（Linux） | 用户 Mac |
| --- | --- | --- |
| `mygo build -platform android/arm64` | 目标被识别，但报错 `no Android SDK: install Android Studio, or set ANDROID_HOME`——缺 SDK，无法构建 | 装 Android Studio（或 cmdline-tools）并设 `ANDROID_HOME` 后可构建 `.apk` / `.aab` |
| `mygo build -platform ios/arm64` | 报错 `iOS apps build on a Mac with Xcode`——Linux 上不可能 | 需 Xcode，可出签名的 `.ipa` |
| `mygo doctor` | android 一项 ✗（no SDK）；无设备 | SDK / Xcode 装好后 `mygo devices` 列出模拟器与真机 |
| `mygo publish` | 未试 | 按帮助可上传到 App Store / Google Play |

**未验证（SDK 门在 Go 编译之前就拦住了）：** 同一个
`cmd/desktop` 主入口能否直接为 android/ios 编译通过。
`cmd/desktop/mygo.go` 里有托盘、Dock 图标、菜单等桌面逻辑
（注释写明 Dock.SetIcon 在非 macOS 是 no-op、托盘失败只记
日志），但在手机目标上是否被 mygo 正确降级，要在 Mac 上
第一次构建时才知道；若不行，需要一个手机专用的 main
（只组装依赖 + 跑 mygo，不建托盘/菜单）。

Mac 上构建命令（推上之后）：

```sh
# Android：先装 Android Studio，设 ANDROID_HOME
export ANDROID_HOME="$HOME/Library/Android/sdk"
cd ~/Documents/zy-drive && git pull
~/tools/go/bin/mygo build -platform android/arm64
# iOS：需 Xcode
~/tools/go/bin/mygo build -platform ios/arm64
```

本地只看首页效果不用构建手机包：

```sh
cd ~/workspace/zy-drive
npm run build --prefix frontend
node scripts/visual-check/check-mobile.mjs            # 390×844 + 430×932
VISUAL_MOBILE_EMPTY=1 node scripts/visual-check/check-mobile.mjs  # 空态
```

## 四、已知的手机坑（本步不解决，方案记在这里）

1. **OAuth 回调**：桌面用 127.0.0.1 环回监听收授权码，手机上
   行不通。方案：系统浏览器回调——iOS
   ASWebAuthenticationSession、Android Custom Tabs + 自定义
   scheme（如 `zydrive://oauth`）。PKCE 与换 token 的代码可复用，
   只换回调传输方式；redirect_uri 需按各家要求重新注册
   （Dropbox/Drive/OneDrive 的移动应用都支持自定义 scheme；
   Box/pCloud 本来就只能自建应用）。但注意：按已决范围，
   手机端根本不做添加流程，所以这项只在“以后万一要在手机上
   加网盘”时才需要；配置导入才是正路。
2. **本地文件夹**：手机沙盒没有任意本地文件夹。上传来源走
   iOS Files 扩展 / Android SAF 文档选择器；“本地文件夹作为
   同步一侧”只留在桌面端。
3. **后台同步**：iOS 后台模式 / Android Doze 下定时同步受限，
   只能打开 App 时同步。定时任务仍由服务器上的 Agent 跑，
   手机只触发与查看运行记录，不在本机托管调度器。
4. **商店内购**：App Store / Google Play 强制数字商品走内购，
   激活码 Pro 模式在手机上违规。手机若收费须走商店内购
   （苹果/谷歌抽成 15–30%）；待用户定：手机首发免费-only，
   还是首发就接内购。

## 五、下一步（按顺序）

1. 用户在 Mac 上第一次构建（Android 和/或 iOS），确认
   `cmd/desktop` 能否直接编译手机目标。
2. 真机验证：粗指针识别、刘海/灵动岛安全区、真实配额数据。
3. 配置导入：传输方式 + 首次启动引导（空态文案随之改）。
4. 点卡片打开网盘：手机浏览视图（列表 + 搜索 + 面包屑）。
5. 文件夹卡片 + 分类九宫格（稿子右屏那套）。
6. 下载 / 上传 UI（上传来源接 Files / SAF）。

## 六、诚实缺口

- 没有手机二进制被构建出来过；构建矩阵全是探针报错，
  不是成功记录。
- 粗指针识别只在无头 Chromium（hasTouch）里验过，真机
  webview 是否报 coarse pointer 未验。
- `cmd/desktop` 为手机目标编译是否通过未验（见 §三）。
- 首页数据只在检查脚本的桩数据上验过，真机真实配额未验。
- 首页 JS 与桌面共用一个包（主 JS 476.46 kB，v0.14.0 时
  463.73 kB，增量主要来自并行的 v0.15.0 桌面工作；手机
  代码本身占比很小）。包是本地加载，暂不拆分。
- 刘海 / 灵动岛安全区只用了 `env(safe-area-inset-*)`，
  未在真机上看过。
- 检查脚本的桩数据在 `check-mobile.mjs` 里，与 `check.mjs`
  的桩网盘各自维护（两份 stub  mount 数据），改一处要记得
  另一处——接受这份重复，保持两个脚本独立可跑。

## 卡片改版（2026-11-26，用户：不像稿子，按对照表改）

- 对照稿子实测：Drive 卡 468×560 竖长、暖米底 #FFF9E9、琥珀环 #FFBB02、白底轨、名字/数字/百分比全粗体、左下“10/15 GB”无小数无空格。
- 根因：初版把卡底+圆环按品牌 logo 色取（Drive 蓝），稿子按每张卡的主题色取（Drive 黄）。
- 改法：卡改竖长 min-height 330px；logo 圆 60px；名字 26px/700、容量数字 24px/700、百分比 22px/700；圆环 88px、描边 10px、底轨白色；⋮ 纯黑 #111；容量行紧凑格式（同单位“10/15 GB”、不同单位“400 GB / 1 TB”）；卡底与圆环走每家主题色（`themeOf`，Drive #F5B81C，其余为品牌色）。
- 字重：手机首页 700 是用户拍板的例外，仅限 .mhome 作用域；桌面去粗体规则不变。
- 检查脚本已同步改断言（Drive 环 #F5B81C、卡底 rgba(245,184,28,0.1)、卡高 ≥ 320、名字 700、环 88px、紧凑容量行），全部通过。
