# ZyDrive 与同类产品权益对比

> 状态：2026-11-25，基线 ZyDrive v0.9.0（GitHub head 2bf10071）。竞品信息来自各产品官网与公开资料当日核实（见文末来源），价格以官网为准。
> 用途：定免费 / Pro 分界线、定价的依据（配合 `docs/monetization.md` 第 5–6 节）。

## 一、五类产品，各自是什么

| 产品 | 一句话定位 | 形态 | 协议 / 价格 |
|---|---|---|---|
| **ZyDrive（我们）** | 多网盘管家：双栏浏览 + 同步引擎 + 自部署 Agent 定时 | 桌面应用（macOS，Win/Linux 待做） | 核心 MPL-2.0 + 界面 MIT，Pro 闭源；Pro 草案 $21/年 或 $42 终身，未开售 |
| **Air Explorer** | 多网盘文件管理器，40+ 云，双栏 + 同步 + 调度 | 桌面 Win/Mac + Android | 闭源；免费 1 账号/云；Pro $21/年 或 $42 终身（每设备） |
| **Alist** | 多存储文件列表 + WebDAV 服务，30+ 驱动 | 服务器端网页应用（Docker 部署） | AGPL-3.0，全免费无付费档；桌面伴侣 alist-desktop 专有约 $8 |
| **rclone** | 云存储的 rsync，70+ 后端 | 命令行（全平台） | MIT，全免费；收费只卖支持合同（$99–$2,499/月） |
| **Mountain Duck / ExpanDrive** | 把云盘挂载成本地盘，在 Finder/资源管理器里直接用 | 桌面挂载客户端 | Mountain Duck $49 一次性/用户/大版本；ExpanDrive 免费版 + Business $29/月 |

Pail（S3 客户端，MIT，桌面 + CLI）只做 S3 系单云，无同步、无多账号，不在主对比表里，只在“云数量”一行作参照。

## 二、权益对比矩阵

状态说明：✅ 已有　🔶 部分 / 有限制　❌ 没有　（Pro）= 需付费版才有

| 权益 | ZyDrive v0.9.0 | Air Explorer | Alist | rclone | Mountain Duck / ExpanDrive |
|---|---|---|---|---|---|
| 支持的云数量 | ✅ 6 家原生（S3/R2、Dropbox、Drive、OneDrive、**Drime**、本地文件夹） | ✅ 40+ | ✅ 30+ | ✅ 70+ | ✅ 15 左右（含 S3、主流云、FTP/SFTP/WebDAV） |
| 多账号（同一云多个号） | ✅ 按 Mount 隔离，不限 | 🔶 免费 1 个/云，Pro 不限 | ✅ 每个存储一个挂载 | ✅ remote 不限 | ✅ 不限 |
| 双栏浏览 / 文件管理 | ✅ 方案 A 全屏双栏 | ✅ 双栏，可切 4 栏 | 🔶 单栏网页列表 | ❌ 命令行（实验性 Web GUI） | 🔶 挂载后用系统文件管理器 |
| 跨云复制 / 传输 | ✅ 同步 Copy + 批量复制，流式不落盘 | ✅ 直传，无流量限制 | ✅ 跨存储复制 | ✅ copy，服务端直传 | 🔶 经挂载盘拖拽（走本地） |
| 同步方向 | ✅ Copy / Mirror / 双向；冲突仅 keep-both 且静默 | ✅ Mirror / Mirror Update / 双向 | ❌ 无同步引擎 | ✅ copy / sync（单向）/ bisync（双向，带冲突标记） | ❌ 无同步引擎（智能按需取文件） |
| 定时 / 调度 | ✅ Agent 调度器（每天/每小时/手动，运行记录，桌面关掉也能跑，自部署） | 🔶 Pro：调度器 | ❌ 无 | 🔶 无内置（靠 cron） | ❌ 无 |
| 批量传输（多选、队列、重试） | ✅ v0.9.0：批量上传/下载/复制，失败只重传失败的 | ✅ 队列、重试、暂停 | ✅ 打包下载、多线程 | ✅ 并行传输、限速、断点 | 🔶 挂载后批量操作走系统 |
| 单文件断点续传 | ❌ 失败从头传（批量级不重传已传完） | ✅ | ✅ 多线程加速 | ✅ 按文件续传 + chunker | ✅ 离线 pin / 智能缓存 |
| 客户端加密 | ❌ 未做 | 🔶 Pro：AES-256（内容+文件名） | ❌ 无 | ✅ crypt（内容+文件名） | ✅ Cryptomator 兼容 |
| 容量显示 | 🔶 5 家有，S3/R2 无（无桶级配额 API） | ✅ 账号页显示大小与剩余空间 | 🔶 各驱动信息页 | 🔶 `ncdu` / about | 🔶 系统盘属性 |
| WebDAV / 挂载为本地盘 | ❌ 未做（P3 候选） | ❌ 本体无（姐妹产品 Air Live Drive 做挂载） | ✅ 服务端 WebDAV，可挂载 | ✅ mount（FUSE）+ serve WebDAV | ✅ 核心能力：挂载成本地盘 |
| 文件预览 / 全文搜索 | 🔶 驱动级搜索有；无文件预览、无全局搜索 | ✅ 跨云搜索（按名/日期/大小） | ✅ 预览丰富（PDF/Office/音视频）+ 全文搜索 | ❌ 无预览 | ❌ 无（用系统应用打开） |
| 桌面平台 | 🔶 macOS；Win/Linux 待做（mygo 可出） | ✅ Win / Mac | 🔶 需自己部署服务器，用浏览器访问 | ✅ Win / Mac / Linux / FreeBSD | ✅ Mac / Win（ExpanDrive 另有 Linux） |
| 移动端 | ❌ 未做（mygo 手机支持刚出，未验证） | ✅ Android 应用 | 🔶 网页版可在手机浏览器用 | ❌ 无官方应用 | ❌ 无 |
| 授权新手友好度 | ✅ 三家官方应用 Client ID 内置，一键授权 + 本机 loopback 自动收码 | ✅ 内置客户端，一键授权 | 🔶 需自建 OAuth 应用或用 token 工具 | 🔶 需自建 OAuth 应用（共享客户端 2026 年起逐步停用） | ✅ 各家账号直连 |
| 本地文件夹作同步端 | ✅ 可挂载，本地只能本机跑 | ✅ PC 与云之间同步 | ✅ 本地存储驱动 | ✅ 本地路径 | ✅ 本地盘 |
| CLI | ✅ `zy-drive`（基础） | 🔶 Pro | ✅ 完整 REST API | ✅ 本体就是 CLI | 🔶 ExpanDrive 有 exfs |
| 开源 | ✅ 核心 MPL-2.0 + 界面 MIT | ❌ 闭源 | ✅ AGPL-3.0（桌面伴侣闭源） | ✅ MIT | ❌ 闭源 |
| 价格（个人） | 未开售；草案 Pro $21/年 或 $42 终身 | 免费；Pro $21/年 或 $42 终身 | 全免费 | 全免费 | Duck $49 一次性；ExpanDrive 免费 / $29/月 |

## 三、结论：我们领先、持平、落后的地方

### 领先或独有的

- **Drime 原生支持**：五家竞品里没有任何一家支持 Drime。Drime 是你的主力盘，这是 ZyDrive 目前最硬的差异化。
- **自部署 Agent 定时**：定时跑在用户自己的服务器上，凭证加密配置包导出导入，不经过任何中间服务器。Air Explorer 的调度器只跑在本机（电脑关了就不跑）；rclone 靠 cron 且要自己写脚本；Alist 没有调度。
- **一键授权的新手体验**：三家官方应用 Client ID 内置 + loopback 自动收码，对新手最友好的一档，和 Air Explorer 同级，比 Alist / rclone（要自建 OAuth 应用）强得多。
- **开源核心**：同定位里只有 Alist / rclone 开源；Air Explorer、Duck、ExpanDrive 全闭源。MPL-2.0 + MIT 的 open-core 是我们独有的结构。

### 持平的

- 双栏浏览、跨云复制、Mirror/双向同步、批量传输、容量显示、多账号——与 Air Explorer 免费版基本对齐；定时调度对齐 Air Explorer Pro。
- 免费 / Pro 定价草案（$21/年、$42 终身）与 Air Explorer Pro 完全同价，这是定价锚，不用再纠结数字。

### 落后的（按对用户的影响排）

1. **云数量 6 vs 40+/70+**：最显眼的差距。Box、MEGA、pCloud、iCloud、阿里云盘、百度网盘等都不在。加一家就是一家驱动的工作量，排期上应优先接用户自己在用的。
2. **无挂载 / WebDAV**：Duck、ExpanDrive、rclone、Alist 都有；我们是 P3 候选（roadmap 16）。没有它，文件只能在 ZyDrive 窗口里操作，进不了 Finder / 其他应用。
3. **无客户端加密**：Air Explorer（Pro）、rclone、Duck 都有；我们未做。备份场景的用户会在意。
4. **无文件预览**：Alist 预览最全（PDF/Office/音视频）；我们连图片在线看都没有，文件只能下载下来看。
5. **平台只有 macOS**：Win/Linux 桌面、移动端都没有；Air Explorer 已有 Win/Mac/Android。
6. **同步冲突只有 keep-both 且静默**：rclone bisync 会标记冲突，Air Explorer 有冲突处理；我们连“发生过冲突”都不告诉用户（roadmap 7）。
7. **单文件断点续传没有**：大文件断一次从头传；Drime 分片会话可续传但没接（roadmap 8）。
8. **分组重启就丢、检查更新未接、英文只覆盖部分页面**：都是已知的诚实缺口（roadmap 附录）。

## 四、对免费 / Pro 分界线的直接影响

- **批量 / 断点续传 / 冲突可视化 / 文件预览已定为免费（2026-11-25 用户拍板）**：批量传输已在 v0.9.0 落地免费版；断点续传、冲突可视化、文件预览待开发，免费。`docs/monetization.md` 第 6 节已按此更新。
- **挂载 / WebDAV（2026-11-25 用户定）**：桌面版开本机 WebDAV 服务没什么用——本机用户直接在 ZyDrive 界面里操作。WebDAV 服务端放 Agent 上才合适（服务器常驻，供其他机器的 Finder、Infuse 连）；桌面端改做 WebDAV 客户端驱动（把 Nextcloud、NAS、Alist 的 /dav 当成一个网盘接进来）；系统级挂载（FUSE）是另一回事，本机挂载才有用，但要另装驱动，后看；分界线待定。
- **客户端加密低优先级（P3）**：上传前本机加密，云端只看到密文；密钥管理是难点（丢密码 = 文件永久打不开），加密后云端搜索 / 缩略图 / 去重失效；分界线待定。
- **定时调度是 Pro 里最值钱的单项**：Air Explorer 把它放 Pro，rclone 用户要自己搭 cron；我们的 Agent 自部署 + 运行记录 + 删文件保护，比 Air Explorer 的本机调度更强，值得作为 Pro 的头牌。
- **云数量是免费版拉新的关键**：Air Explorer 免费版 40+ 云、1 账号/云；我们免费版 6 云、账号不限。拉新时“支持的云多不多”比“一个云能挂几个号”更直观，接新驱动应优先于放开账号数。

## 来源

- Air Explorer 官网首页与版本功能页（airexplorer.net，2026-11-25 核实）：40+ 云、免费 1 账号/云、Pro $21/年 或 $42 终身、调度器/加密/CLI/AI 为 Pro。
- Alist：GitHub AlistGo/alist（AGPL-3.0）功能列表、Railway 部署页：30+ 驱动、WebDAV、预览、全文搜索、全免费。
- rclone：rclone.org 官网：70+ 后端、copy/sync/bisync、mount、crypt、MIT 全免费。
- Mountain Duck / ExpanDrive：官网与 2026-11 评测汇总：Duck $49 一次性/用户/大版本；ExpanDrive 免费版 + Business $29/月；两者核心能力均为挂载成本地盘，无同步引擎。
- Pail：github.com/DejavuMoe/Pail（MIT，v0.7.0）：S3 系桌面 + CLI，无同步、无多账号。
