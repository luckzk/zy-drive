# ZyDrive 下一步功能规划（Next）

> 状态：规划稿，2026-11-25。只规划、不写代码。v0.8.5 正在修（系统代理、授权组件内测试、“自己的应用”重设计、Dropbox 容量、hover 裁剪、Dock/.app 图标、“已失效”追踪），本规划不重复那些内容。
> 版本基线：本地 v0.8.4（GitHub head b8498fa9）；v0.8.5 改动在工作区，未推送。
> **v0.9.0 落地（2026-11-25）：本文件第 3 节的五项已全部做完，见文末“附：v0.9.0 落地记录”。**
> **v0.9.1 落地（2026-11-25）：单文件断点续传（Drime 先行）、Box/pCloud/MEGA 三家新驱动、文件预览两档，见文末“附：v0.9.1 落地记录”。**
> **v0.9.2 补丁（2026-11-26）：MEGA 表单三处 UI 修正（测试通过变绿、凭证说明改问号悬浮提示、输入框焦点绿环左侧裁剪修复）+ Dropbox 一键授权自动换取 bug 修复，见文末“附：v0.9.2 补丁”。**
> **v0.9.3 补丁（2026-11-26，已推，提交 b3892ed9）：MEGA 挂载加载失败的诊断与恢复——LoadMounts 记真实错误、会话失效标 auth_expired、文件浏览重试走 TestMount，见文末“附：v0.9.3 补丁”。根因后经用户确认是 MEGA 账号被锁（重置密码后恢复，旧会话继续有效），不是代码 bug。**
> **v0.10.0（2026-11-26，未推）：主页交互优化——长按拖拽网盘换分组 + 文件右键菜单（删除/重命名/移动，三个新后端方法），见文末“附：v0.10.0”。另修了一个共享样式真 bug：`.dialog-actions` 的 sticky 底栏在短对话框里会盖住文案行下半截。**
> **v0.11.0（2026-11-26，未推）：侧栏网盘排序 + 拖拽丝滑化——长按拖拽可改顺序（第 8 个拖到第 4 个、其余下移带过渡动画），不只是换分组；新增 `sort_order` 字段（迁移 v24/v25）与 `MoveMount` 后端方法，见文末“附：v0.11.0”。**
> **v0.12.0（2026-11-26，未推）：版本历史真实现（Drive/S3/Dropbox）+ Copier 服务端复制（S3/Dropbox）+ OneDrive 上传 409 修复 + 拖拽分组标题跟随，见文末“附：v0.12.0”。**
> **v0.13.0（2026-11-26，未推）：文件浏览页 UI/UX 批——命令面板居中修复、主面板去重容量、顶栏去掉“新建同步”、列表/网格切换、详情页定义列表重排、右上角 Toast，见文末“附：v0.13.0”。全屏预览 overlay（调研已定）仍在排队，见 v0.13.0 附录末尾。**
> **v0.14.0（2026-11-26，未推）：文件预览全屏 overlay（P1）——深色遮罩 + 逐文件切换 + 自适应工具条 + 图片/视频/音频/PDF.js/代码/Markdown 查看器 + 右侧信息面板，见文末“附：v0.14.0”。Inspector 的 glance 保留。**
> **v0.15.0（2026-11-26，已推，提交 3dbb44ce）：主页面 UI 审计 Top 5 全做——新建文件夹 + 上传文件入口、列表排序 + 全选、Inspector 操作按钮、批量栏上移 + 移动到…、命令面板边输边搜 + 方向键导航；另修同区 S 项（刷新 busy 态、glance 240px、--selected 统一选中色、刷新全部容量改名、同步任务角标），见文末“附：v0.15.0”。另带手机首页存储卡片 v1（Dribbble 版式、主题色、竖长卡、粗体）+ 卡片改版（主题色按卡取、紧凑容量行）。**
> **v0.16.0（2026-11-27，已推，提交 8ada9cf5）：手机首页第二轮——顶栏居中粗体 + 深薄荷绿块、顶栏菜单/搜索、筛选条 全部/云盘/本地、分区标题加粗 + 查看全部切轮播/全列表、底部黑导航 网盘/同步/传输/设置（均接真实接口，无数量角标）、次要文字改中性灰；铃铛与最近上传未做（无数据源），见 docs/mobile-app.md。**
> **v0.17.0（2026-11-27，已推，提交 00b26d73）：设置页 Drime 改版——顶栏菜单（通用/网络/网盘/同步默认/缓存/Agent/关于）+ 方向滑动（220ms）+ 内饰 Drime 化（卡片行、分段选择、开关、红框删除；无新增设置）；节标题 700 是 de-bold 第二个特批例外；Demo v8 不再是设置页对照标准。**
> **v0.17.1（2026-11-27，未推）：同步文件根修复（Scan 先 Stat 根、文件当单节点种子、空计划拦截、runNow 忙态/防重入/报数/自动展开运行记录）+ 设置页跟进（滑动 350ms/40px、“高级”全拆常显行、设置按钮移到左下角 Agent 卡片下）+ 双栏选中行去重（右侧对勾拿掉）。**

## 1. 现状盘点（到 v0.8.4/v0.8.5，一句话一个）

- **驱动**：S3/R2、Dropbox、Google Drive、OneDrive、Drime、Box、pCloud、MEGA、本地文件夹九家全部原生真实现（官方 API，无 rclone；MEGA 走 go-mega 客户端库（已加进 go.mod，pin c6acd6a，与 Alist/rclone 同一 commit））。
- **浏览**：双栏文件浏览、按需 Inspector、分组挂载（分组 session-only，未持久化）、多账号按 Mount 隔离。
- **上传**：拖拽上传（mygo onFileDrop，流式、200ms 进度节流、目标文件夹选择、只重传失败项），无上传按钮/系统文件选框；**单文件断点续传已做（v0.9.1，Drime 先行）**，其他云未做。
- **下载/复制**：单文件下载、复制有驱动级能力；批量上传/下载/复制未接线。
- **同步**：SyncJob 持久化、双栏选根、双栏多选（每项各建一个任务，两边多选笛卡尔积，批量只创建不运行）、默认排除规则 9 条、比较/计划/预览/执行引擎已实现、双向冲突 keep-both（唯一已实现的策略）。
- **同步执行**：本机 RunJob 能跑已确认的计划；Agent 端调度器未接（`internal/agentapi/server.go` 有 TODO），定时任务跑不起来。
- **授权**：三家官方应用 Client ID 内置、一键授权默认入口、自建应用单独存放可切换；本机 loopback 回调自动收 code；v0.8.5 正在把测试收进授权组件、把手动粘贴收进兜底。
- **容量**：Dropbox/Drive/OneDrive/Drime/本地五家有 Quota，侧栏“已用 X / 共 Y”+ 进度条；S3/R2 故意不实现（无桶级配额 API）；Drime 按工作区返回、旁标工作区标签。
- **传输**：Queue + Worker（并发参数存在）+ Limiter 类存在；但设置里的并发/限速现在不生效（引擎固定 2 路、无限速）。
- **设置**：主题/语言（英文只覆盖导航/顶栏/设置页）/同步默认/网盘/Agent 列表/关于；检查更新未接入；开机启动接 mygo API、未打包验证。
- **代理**：v0.8.5 在做系统代理检测 + 设置页代理选项（跟随系统/手动/直连）+ 测试代理按钮。
- **图标**：logo 已定稿为用户自发版本（裁画框）；`make app` 打带图标 .app 在 v0.8.5 做。
- **真实验证**：真实账号端到端只测过 S3/R2；Dropbox/Drive/OneDrive/Drime 全是 mock 契约测试。
- **许可证**：仓库无 LICENSE 文件；开源还是闭源商业未定。
- **域名**：zydrive.com 已被他人注册；产品名仍叫 ZyDrive，域名策略未定。

## 2. 候选功能清单（按价值 × 成本排序）

> 工作量：S ≤ 半天–1 天，M 2–4 天，L 1 周以上（以当前一人 + agent 协助的节奏估算，非承诺）。
> 优先级：P0 下一批必做，P1 尽快，P2 可以等，P3 方向性/看用户决策。

### P0

| # | 功能 | 解决什么问题 / 用户是谁 | 工作量 | 前置条件 | 坑 |
|---|---|---|---|---|---|
| 1 | **Agent Runner / 调度器接线**：Agent 真能按时间表跑同步任务（定时、立即执行、运行记录、失败重试与通知） | 现在同步任务只能本机手动点“立即同步”，“服务器部署 Agent 跑定时任务”是产品立项时的核心承诺，至今是空的；用户是每天要自动同步的人 | L | 无（API、Job 模型、RunJob 都已存在；只差 agentapi server 里的调度器接线 + 前端运行历史） | Agent 与桌面各有自己的 SQLite/Token，凭证如何安全下发到 Agent 是最大设计点；Agent 挂了、任务失败了要让桌面知道，不能静默丢；定时任务跑在服务器上，流量不走桌面 |
| 2 | **同步并发与限速真正生效**：设置里的并发数、限速不再是摆设 | 用户已在设置里填了并发 4、限速 0，现在填了没用；属于“有 UI 没功能”，用户迟早会发现 | S | 无（Worker 已接受并发参数、Limiter 类已存在，只差接线） | 并发调太高会触发各家 429（Drime 429 可罚 15 小时）；限速要按 Mount 分还是全局分，需先定规则 |
| 3 | **原地更新凭证（重新授权不建新盘）**：Token 过期/换号时在原 Mount 上重新授权，任务、分组、缓存全保留 | 现在重新授权 = 新建一个同名盘，旧盘手动删，同步任务还挂在旧盘上；每个 OAuth 用户迟早会遇到 Token 过期 | M | 无（BeginOAuth/Exchange 流程已存在，只差“更新到现有 Mount”的 Bind + 前端入口） | 正在跑的任务持有旧 Token 时要不要先停；多账号同名盘之间不要认错 Mount |

### P1

| # | 功能 | 解决什么问题 / 用户是谁 | 工作量 | 前置条件 | 坑 |
|---|---|---|---|---|---|
| 4 | **批量上传/下载/复制接线**：多选文件/文件夹后批量传，队列里看进度、失败重传 | 现在只能拖拽单批上传；双栏多选只服务于建同步任务，选完文件却传不走，用户会觉得多选是半成品 | M | 传输队列、Worker 已存在；双栏多选已存在 | 大批量跨云复制要走流式 pipe，不能落盘中转；失败重试与断点要与第 8 项配合 |
| 5 | **检查更新 feed 接线**：设置页“检查更新”真能查到新版本 | 现在明确写着“暂未接入”；没有它，用户永远不知道有新版，只能靠聊天通知 | S | 需要一个更新源（GitHub Releases 就是现成的；mygo Updater 需要打包时配置 feed） | 免费方案（GitHub Releases）与签名验证要一起定；不要在用户没问时弹更新 |
| 6 | **Drime 工作区选择器**：加 Drime 盘时从 `GET /me/workspaces` 拉工作区列表点选，不用手填 ID | 现在工作区 ID 要用户自己找出来手填数字（8762 这种），新手根本找不到；且填错就挂到错误的区 | S | Drime API 有该端点（需核实返回结构） | 个人空间 id 0 与团队工作区的展示要分清；向导里要处理“只有一个工作区”的情况（直接默认，不打扰） |
| 7 | **同步冲突可视化**（免费，2026-11-25 用户定）：双向同步出冲突时，列表里能看到“两边都改了”，逐项选留哪边、留冲突副本还是合并 | 现在冲突策略只有 keep-both 且是静默的；用户不知道冲突发生过，也没得选 | M | 比较/计划引擎已能识别冲突（conflict.go 已存在） | 冲突判断依赖时间戳+哈希，跨云哈希口径不一会误判；UI 上不要让新手看到“冲突”两个字就慌 |
| 8 | **单文件断点续传**（免费，2026-11-25 用户定）：大文件分片上传中断后，重启应用能接着传，不是从头传；Drime 先行（走 S3 分片），再扩到 OneDrive uploadSession / Drive resumable | Drime 大文件走 S3 分片，现在失败就整个 abort；用户传大文件（aigo 的剧集动辄几 GB）时断一次就重传一次 | M | transfer/resume.go 已有 Resume 骨架；Drime 分片会话 API 已存在 | uploadId 与已传分片号要持久化到 SQLite；Drime 的分片会话有效期未知，需先验证；失败重试不能触发 429 罚时。**已做（v0.9.1，Drime 先行），见文末落地记录；其他云待扩。** |

| 18 | **文件预览**（免费，2026-11-25 用户定）：图片、视频、PDF、Office 文档在应用内直接看，不用先下载 | 现在文件只能下载下来看；Alist 预览最全，是新手每天都会碰到的能力 | M | 各家驱动已有下载流；图片/视频可先走直链播放 | 大文件视频要边下边播（流式），不能先下完；Office 预览需转码或嵌系统查看器；预览走直链时注意 Token 别泄露到 URL。**已做（v0.9.1，两档：应用内直接预览 + 系统应用打开），见文末落地记录；`zydrive-preview://` 需 Mac 实测。v0.14.0 加了第三档全屏 overlay（深色遮罩 + PDF.js + 代码高亮 + Markdown），见文末“附：v0.14.0”。** |

### P2

| # | 功能 | 解决什么问题 / 用户是谁 | 工作量 | 前置条件 | 坑 |
|---|---|---|---|---|---|
| 9 | **完整英文翻译**：全站文案 en/zh 两套，不再是导航/顶栏/设置页有英文、其他页中文 | 现在语言切换是半成品；要出海或给非中文用户用时必须补齐 | M | i18n 框架已有（部分页已接），只差逐页补 key | 容易漏：错误提示、空态文案、向导里的长说明最容易漏；翻译不要机翻直出 |
| 10 | **文件去重**：同一网盘内按哈希/大小+名找出重复文件，预览后批量删。详细设计见文末附录（2026-11-25） | 用户多盘之间、同步之后最容易产生重复文件；是 Air Explorer 类产品常见的“清理”入口 | M | 各家驱动的哈希口径不一，需先定“什么算重复”的规则；**各云去重逻辑不同，很可能要每云单独做**（哈希获取、比较、删进回收站的语义都不同） | 删文件是不可逆操作，必须预览+二次确认；跨云哈希对不上时只能按大小+名提示“疑似重复”，不能直接判死刑；加密文件只能用本地索引的明文哈希比，密文哈希无用 |
| 11 | **传输速度统计**：传输胶囊与任务详情里显示实时速度、剩余时间、本日/本周传输总量 | 现在只有进度条，没有速度；用户传大文件时最想知道“还要多久” | S | Worker 已有 RecordProgress（done + speedBps 字段已存在），只差前端展示 | 速度是抖动的，要做滑动平均，不然数字乱跳；跨云速度差异大，不要拿来做排名 |
| 12 | **Google 应用发布与验证**：把 ZyDrive-app 从测试状态发布、走 Google 验证 | 测试状态下 Refresh Token 7 天过期、只有测试用户能授权；不发布，Google 一键授权永远是“只能作者自己用” | M（主要是流程时间，不是代码） | 需要 Google Cloud 的发布流程 + 隐私政策页 + 域名（与域名决策绑定） | Google 验证要审核隐私政策与应用用途，周期以周计；验证期间测试用户的 Token 仍会过期，需提前告知用户 |
| 13 | **S3 兼容存储的容量估算**：S3/R2 桶没有配额 API，用 List 采样估算已用容量 | S3/R2 现在容量栏是空的；用户看不到 R2 用了多少 | M | List 分页已存在 | 全量 List 大桶会很慢很贵（请求费），只能采样+估算并注明“估算值”；不要为了容量去扫整个桶 |

### P3（方向性，先定方向再排期）

| # | 功能 | 解决什么问题 / 用户是谁 | 工作量 | 前置条件 | 坑 |
|---|---|---|---|---|---|
| 14 | **开源 vs 商业 / 许可证定夺**：定 LICENSE、定 README 里的定位 | 仓库现在无 LICENSE，法律上等于“保留所有权利”；不做决定就没法公开推广、也没法收钱 | S（决策重、代码轻） | 用户拍板 | 选错难回头：MIT/Apache 易推广但难商业化；闭源则失去社区；与内置共享 OAuth 客户端的风险有关（公开客户端被滥用时，闭源/开源的应对不同） |
| 15 | **域名策略**：zydrive.com 已被占，定官网/文档/更新 feed 用什么域名 | 检查更新、Google 发布验证、官网都需要域名；没有域名这些都卡着 | S（决策重） | 用户拍板；可注册性需在注册商处核实（本机 dig 不可信） | 不要在没核实的情况下宣布域名；.com 之外的后缀（.app/.cloud）要与品牌一致 |
| 16 | **挂载 / WebDAV**（分两个方向，2026-11-25 用户定）：① WebDAV 服务端放 Agent 上——服务器常驻，把挂的网盘通过 WebDAV 暴露给其他机器的 Finder、Infuse 等；桌面版开个本机 WebDAV 服务没什么用（本机用户直接在 ZyDrive 界面里操作，绕一圈没意义）。② 桌面端做 WebDAV 客户端驱动——把 Nextcloud、NAS、Alist 的 /dav 这类 WebDAV 服务当成一个网盘接进来，等于不写新驱动就多了一类云。③ 系统级挂载（FUSE：macFUSE / WinFsp）是另一回事，本机挂载才有用，但要用户另装驱动、Apple Silicon 批内核扩展，成本高，后看 | 服务器常驻才有意义的 WebDAV 服务 vs 桌面端接 WebDAV 云 | L | Provider 接口已抽象；WebDAV 服务层与客户端驱动都是标准协议（Go 有标准 webdav 包） | 认证、权限、性能（WebDAV 客户端爱发大量小请求）都要设计；Drime 没有 delta，WebDAV 的 PROPFIND 会很慢；分界线待定（免费还是 Pro 未定） |

| 19 | **客户端加密**（低优先级，P3，2026-11-25 用户定）：上传前在本机用密码加密（AES-256，内容+文件名），云端只看到密文，下载时解密；rclone crypt / Air Explorer Pro / Cryptomator 同款。密码学参考与已定方案见 `docs/client-encryption.md`：对照见第一至四节，给 ZyDrive 的建议见第五节，文件名变长方案见第七节（按云选编码 + shortening + 目录名可选不加密 + 文件名加密可关），本地解密索引见第八节（上传时顺手建索引、SQLite 加密存放），待决策问题见第九节（格式自研还是兼容 Cryptomator、主密钥备份、恢复码、解密永远免费等 12 条） | 备份场景的用户在意“云厂商也看不到内容” | M | 加密原语标准（AES-256-GCM + PBKDF2 派生密钥），只差密钥管理与各驱动的接线 | 密钥管理是难点：丢了密码文件永远打不开；加密后云端搜不到内容、缩略图与去重失效（本地索引补回来，见第八节）；分界线待定（Air Explorer 放 Pro；但解密永远免费，见第九节第 11 条） |
| 17 | **分组持久化**：分组不再 session-only，重启后还在 | 现在分组重启就丢，用户分好组下次打开没了 | S | Mount 已有 Group 字段，只差持久化 + 迁移 | 旧数据（已有分组但未持久化的）要不要迁移，需先定 |

## 3. 推荐的下一批（v0.9.0 建议）

挑 5 项，理由是：把“已承诺但未交付”的核心补齐（1、2、3），再把用户每天都会碰到的两个痛点修掉（4、6）；不选 12、14、15 因为它们是决策/流程不是功能，且 12、15 依赖 14、15 的拍板。

| # | 功能 | 为什么是这批 | 验收标准（一句话） |
|---|---|---|---|
| 1 | Agent Runner / 调度器接线 | 立项时就承诺的“服务器跑定时任务”，现在同步任务只能手动点，是产品最大的未交付 | 在服务器 Agent 上建一个每天 02:00 跑的同步任务，桌面关掉，第二天能在运行记录里看到它跑完了 |
| 2 | 同步并发与限速真正生效 | 设置里已有 UI 但没功能，属于“有 UI 没功能”，必须补，否则用户信任会掉 | 设置并发 4、限速 1MB/s 后跑一次同步，实测并发数与速度与设置一致 |
| 3 | 原地更新凭证 | 每个 OAuth 用户都会遇到 Token 过期；现在过期 = 丢任务重建盘，是最伤的体验 | 在原盘上重新授权一次，同步任务、分组、缓存全在，不用新建盘 |
| 4 | 批量上传/下载/复制接线 | 双栏多选已做完却传不走文件，是半成品；且是用户每天用的基础能力 | 选 10 个文件点批量上传，队列里能看到进度，断网重传后不从头传 |
| 6 | Drime 工作区选择器 | 用户手填工作区 ID 是现在加 Drime 盘最大的门槛；且 Drime 是用户的主力盘 | 加 Drime 盘时能从列表里点选工作区，不用手填数字；只有一个工作区时直接默认 |

为什么不是别的：

- 不选 5（检查更新）：重要但不紧急，且依赖域名/发布渠道的决策（14、15）；可以放在 v0.9.1，与 14、15 一起定。
- 不选 7（冲突可视化）：有价值，但用户还没报过冲突问题；等 Agent 跑起来、同步真正每天跑之后，冲突才会多起来，那时再做更有针对性。
- 不选 8（Drime 断点续传）：有价值，但需要先验证 Drime 分片会话有效期；且批量传输（4）先做会把传输队列与重试的基础打好，8 是 4 的后续。
- 不选 9（完整英文）：翻译量大但不影响当前用户（中文用户）；等开源/出海方向定了再做，避免翻完又改文案。
- 不选 12（Google 发布验证）：是流程不是功能，周期以周计，可以和 v0.9.0 并行走流程，不占开发排期；但要先有域名（15）。
- 不选 14、15（开源/域名）：是决策，不是功能；v0.9.0 开发的同时用户拍板，v0.9.1 再落地。

## 4. 需要用户拍板的事项

| # | 问题 | 默认建议 | 不拍板会卡住什么 |
|---|---|---|---|
| 1 | **开源还是闭源商业**（含 LICENSE 选什么） | 先 MIT 开源：当前是个人项目、靠口碑传播的阶段，开源最容易拿到用户与反馈；商业化以后再通过“桌面免费 + Agent/高级功能收费”或捐赠走。许可证选 MIT（最宽松、最常见）。 | 仓库无 LICENSE 就等于没法合法分发；检查更新、官网、推广都没法启动 |
| 2 | **域名**（官网/文档/更新 feed 用什么） | 查 `zydrive.app`、 `zydrive.cloud`、 `zy-drive.com` 是否可注册，优先 `.app`（与产品名最贴，且 .app 强制 HTTPS）；zydrive.com 已被占就不要再纠结它 | Google 发布验证要域名；检查更新 feed 要域名；官网要域名 |
| 3 | **Google 发布验证要不要做**（现在做还是等用户量上来再做） | 现在就启动流程：验证周期以周计，越早启动越好；发布期间作者自己用测试用户先顶着，7 天过期问题用“重新授权”顶一下 | 不发布，Google 一键授权永远只能作者自己用；发布了才能给别人用 |
| 4 | **Agent 跑在哪里**（用户自己的哪台服务器、还是先跑在 Mac 上验证） | 先跑在用户现有的服务器上（已有 tg-vault、zy-project-board 的那台 161.153.90.122，或另外一台），用 Docker 跑 zy-drive-agent；Mac 只做验证，不做长期运行 | Agent Runner 做完后没有地方部署验证，等于没做 |
| 5 | **同步任务默认跑在哪里**（本机还是 Agent） | 默认本机：新手先在本机跑通，理解同步是怎么回事；Agent 是进阶选项，在任务里显式选“在 Agent 上跑” | 不定默认值，新手建任务时会困惑；Agent 的凭证下发方案也定不下来 |
| 6 | **要不要做完整英文翻译**（现在做还是等开源/出海方向定了再做） | 等开源决策（事项 1）定了再做：如果开源面向全球用户就做；如果只做中文个人用就不做 | 现在做可能白翻（文案还会大改）；不做则出海无从谈起 |

## 附：已知的诚实缺口（做下一批时顺手确认，不要另开项目）

- 三家一键授权、Drime 工作区容量、Dropbox 容量均只有 mock 测试，真实账号端到端未测（只有 R2 测过）；下一批做完后请用户在 Mac 上用真实账号走一遍。**v0.9.1 补充：新增的 Box/pCloud/MEGA 三家与所有一键授权流程也全是 mock；MEGA 的 go-mega 模块已加（pin c6acd6a，stub 已删、真适配器已编译进默认构建）；pCloud 欧洲区换 Token 端点待真实欧洲账号验证；`zydrive-preview://` 自定义 scheme 与 Quick Look/system-open 需 Mac 实测；断点续传只有 Drime。**
- 首屏仍把容量算进必需数据，网盘多且某盘容量挂起时骨架最长等 N×15 秒（v0.8.4 遗留）。
- 设置页仍有 `<details>`“高级”折叠（v0.8.3 范围外遗留）。
- 批量创建的同步任务只创建不运行、无逐项预览（有意为之，见 v0.8.3）。
- 分组 session-only（见候选 17）。
- 开机启动需打包后真机验证（`make app` 在 v0.8.5 做）。

## 附：v0.9.0 落地记录（2026-11-25）

五项全部做完并验证通过（版本号 0.9.0，五处一致；npm build JS 397.89 kB / gzip 123.96 kB；make build 三二进制 0.9.0；go vet、go test 全过；visual-check 三种模式全过；dev-shell smoke `/api/health` = 0.9.0）。

1. **Agent Runner / 调度器**：`internal/agentrun` 新包（调度器 + 执行器 + 运行记录 + 失败 10 分钟后重试一次 + 删文件保护——定时任务含删除计划时直接失败、文件不动）；Agent 的 job/run 存 SQLite；配置包加密下发（PBKDF2 100k + AES-256-GCM，口令 ≥8 位），桌面导出 → 用户自己拷到服务器 → `zy-drive-agent import`，绝不经过我们的服务器；部署见 `docs/agent-deploy.md`（Docker + systemd）。验证：本机真二进制 e2e——假 S3 后端、每天 02:00 的任务改成短周期定时，手动触发 2 个文件跑完、定时触发第 3 个文件跑完，运行记录两条都在（注：云端是假 S3，S3 驱动本身已在 v0.4.1 用真实 R2 测过；未在用户服务器 161.153.90.122 上部署验证）。
2. **并发与限速**：`transfer.Semaphore`（可实时改上限）+ 全局限速器（单例，不是每路各限）；同步引擎并行复制 + 限速读；设置保存后立即生效。验证：测试实测——4×200ms 读并发 4 在 650ms 内跑完、峰值读 4；200 KB 以 100 KB/s 限速约 2s；并发 1 退化为串行；删除仍串行且最后执行。Drime 429 罚 15 小时的提醒写进了设置页文案。
3. **原地更新凭证**：`UpdateMountCredentials`（同一事务换凭证 + 清缓存，Mount ID/任务/分组/缓存全保留，失败不换）。验证：测试——换桶后同一 ID、分组“工作”保留、任务仍指向它、缓存与 delta 游标清掉、重启后还在；死端点探活失败时旧凭证原样不动；错厂商/本地盘拒绝。
4. **批量上传/下载/复制**：`BatchDownload`/`BatchCopy`（文件夹保结构、流式 pipe 不落盘、跳过缺失项并给原因、拒绝自复制）；失败重传只传失败的（`Requeue`，已传完的永远不重排）；前端文件浏览多选栏 + 复制目标选择框 + 传输胶囊重试按钮。验证：测试——10 文件批量复制/下载端到端全过；重试后已传完 4 个文件的 mtime 不变。**诚实缺口：单文件断点续传仍未做**（无分片读取，失败文件从头重传；批量级“不重传已传完”是做到的）。
5. **Drime 工作区选择器**：`GET /me/workspaces` 拉列表点选（个人空间 ID 0 置顶、团队工作区带角色），单工作区自动默认，手动输入 ID 留作兜底。验证：向导里点选 sese（8762）通过；mock 契约测试通过。真实 Drime 账号仍未端到端测过。

顺手修的三个同步引擎潜 bug（都是定时任务必经之路，测试发现的）：① 本地驱动把同步路径当绝对 OS 路径（`/sub/b.txt` 被当成根目录下的文件）；② Compare 把仅源端有的目录当成新增文件下发（上传目录会报错）；③ 递归扫描把嵌套路径拼重（`/sub/sub/b.txt`）。修后测试与真二进制 e2e 均通过。另：**空源目录不同步**（目录靠文件上传隐式创建），记为已知限制。

v0.9.0 之后仍未做的诚实缺口：真实账号端到端仍只测过 S3/R2（Dropbox/Drive/Drime/OneDrive 全是 mock）；单文件断点续传未做；空源目录不同步；首屏骨架仍可等 N×15 秒；设置页仍有 `<details>`“高级”折叠；检查更新未接；开机启动需打包后真机验证；英文只覆盖导航/顶栏/设置页。

## 附：v0.9.1 落地记录（2026-11-25）

三项全部做完并验证通过（版本号 0.9.1，五处一致；npm build JS 417.81 kB / gzip 129.23 kB；make build 三二进制 0.9.1；go vet、go test 全过——21 个包；visual-check 三种模式全过；dev-shell smoke `/api/health` = 0.9.1）。

1. **单文件断点续传（Drime 先行）**：uploadId + 已完成分片号持久化到 SQLite（`upload_sessions` 表，migration v21，主键 mount_id + ref + size）；中断/重启后从下一个未传分片续传，不从头传；分片读改成 ranged 读（`io.NewSectionReader`）；每传一片先记 MD5 并持久化、再 PUT，续传时用**本地记的 MD5**重算校验（不认服务器 ETag——Drime 的 ETag 是不透明值），文件被改过（同大小、内容变了）能发现并重传该片。Drime 分片会话有效期：文档里没写，实测也拿不到——保守处理：会话失效（get-uploaded-parts 报 400/404/410）就删行建新会话、只重传缺失分片，旧会话由服务器自己过期；失败时会话保留、永不 abort。只对本地文件（可 seek）+ Drime 生效；小于 5 MiB、无 store、无 MountID 时退回普通上传；与 v0.9.0 的并发信号量 + 全局限速器协同（只有 ReadAt 计进度/限速）。验证：测试 8/8——中断后续传只补缺失分片（12 MiB、3 片，断在 2 片后只补 1 片）、重启后会话还在、会话过期重建、改过内容 MD5 发现、ResumeState 种子进度、小文件/无 store 退化。**诚实缺口：只有 Drime 能断点续传**，OneDrive/Dropbox/Drive/Box/pCloud/MEGA 的大文件仍是失败重传；Box 的分片会话、MEGA 的分块上传都没持久化。
2. **新驱动 Box、pCloud、MEGA（9 家）**：三家都接 Provider 接口 + Caps + Quota + mock 契约测试；向导第一步九宫格（品牌 logo 本地打包，不走 CDN）、容量接 QuotaBar、README 驱动列表 6→9。
   - **Box**：OAuth 2.0 + REST；列表/下载/上传（<20 MiB 直传、≥20 MiB 分片会话：建会话 → PUT 各片带 `Digest: sha=` → commit，失败 DELETE 掉会话）/搜索/容量（`GET /users/me`）。向导走 OAuth 流程，固定重定向 `http://localhost:57103`、scope `root_readwrite`、换 Token 必须带 Secret。测试 6/6（含 25 MiB 分片上传 4 片、Digest 校验、401 刷新重试、分享链接）。注册指南 `docs/oauth-apps/box.md`。**没有官方应用**：一键授权卡片显示“还没配置”，只能用自己的应用。
   - **pCloud**：OAuth 2.0 + REST；美区 `api.pcloud.com` / 欧区 `eapi.pcloud.com`，向导里可选手/自动识别（auto 先探美区，不行手动切欧洲；不做回调域名嗅探）；列表/下载（`getfilelink` 直链，接预览 tier 1）/上传/搜索/容量（`userinfo`）。测试 10/10（含 25 MiB 大文件上传、区域探测、2004 已存在文件夹）。注册指南 `docs/oauth-apps/pcloud.md`。**没有官方应用**，同 Box。**诚实缺口：欧洲区换 Token 的端点（`https://eapi.pcloud.com/oauth2_token`）是按 host 对称推断的，没在真实欧洲账号上验证过。**
   - **MEGA**：私有协议 + 客户端加密，不走标准 REST。用 `github.com/t3rm1n4l/go-mega` 包在 Provider 接口后面（与 S3 用 aws-sdk-go-v2 同一个模式）。邮箱+密码登录（可选 2FA），向导是一页表单（测试+保存同页，探活失败也可保存为未验证）；密码永不存盘，只存导出的会话（session ID + base64 主密钥，加密存放），重启后凭会话登录。测试 7/7（假客户端：2.5 MiB 上传/下载往返、分块 seek、会话导出重登、容量、搜索、分享）。**2026-11-26 用户拍板加依赖**：go-mega 已加进 go.mod（v0.0.0-20260717075258-c6acd6a5bd04，与 Alist/rclone 同一 commit），stub 已删、真适配器已编译进默认构建（go build + 全量测试通过）；搜索是客户端全树遍历（O(tree)，大账号会慢）；上传必须已知大小。
   - OAuth 接线：Box 固定端口 57103、pCloud 57104（端口被占就明确报错、不悄悄换随机口）；pCloud 回调会丢 `state`，单挂起流程时容忍缺失 state；PKCE 只在 Dropbox/Drive/OneDrive 用，Box/pCloud 不用。另修了一个真 bug：`closeListener` 只关 `server.Close()`，CancelOAuth 若在 Serve 启动前跑会漏关监听器、端口一直占着——现同时关 `p.listener`。
3. **文件预览（免费，两档）**：Inspector 里点文件就出预览（在 mime 行下、“高级”上）。
   - **应用内直接预览**：图片（`<img>`，maxWidth 100% / maxHeight 240）、视频/音频（`<video>`/`<audio>` 流式播放，`preload="metadata"`，不先下整文件）、PDF（`<iframe>` 内嵌）、纯文本/代码/Markdown（512 KiB 截断 + 自写最小 Markdown 渲染，先转义再套格式，不引依赖）。直链优先：S3（15 分钟预签名）、Dropbox（临时链接）、OneDrive（downloadUrl）、pCloud（getfilelink）四家有 DirectURL；拿不到直链时走代理。**代理走 `zydrive-preview://` 自定义 scheme**（不是 127.0.0.1 HTTP——前端本身跑在 `mygo://` 上，http 子资源会被当混合内容拦），Go 端注册协议处理，Token 16 随机字节、10 分钟有效、窗口内可复用（视频 seek 要多次请求，单次有效会断播）；Range 请求支持（`bytes=<start>-` / `<start>-<end>`），不支持 ranged 读的驱动 seek 时从头重下再丢弃前缀（慢 seek，已在代码里注明）；限速只计真正吐给 webview 的字节，不计丢弃的前缀。验证：Go 测试全过（整文件 + Range + 416 + 过期 Token + 文本截断 + 二进制拒显）；visual-check 五档断言全过（图片真渲染、代码 `<pre>`、Office 只给系统按钮、视频降级文案、PDF 有 iframe）。
   - **系统应用打开**：Office/PSD 等应用内渲染不了的，先下载到本地预览缓存，再调系统默认应用打开——Mac 优先 Quick Look（`qlmanage -p`，打不开再落到 `open`），Windows ShellExecute，Linux xdg-open；另有“在 Finder/资源管理器中显示”入口（Mac `open -R`、Windows `explorer /select`、Linux 打开父目录）。预览缓存按 2026-11-25 用户确认的规则实现（见文末附录）：只存系统打开那档的副本（应用内预览不落盘）；位置是各平台应用缓存目录；键是 mount_id + 远端路径哈希；SQLite 清单表记大小/远端 mtime/最后访问，启动时清单与磁盘对账；写入原子化（临时文件 + rename）；复用比大小与远端 mtime；超上限按 LRU 淘汰（5 分钟内访问过的不淘汰）；写完置只读 0444；设置 → 缓存一张卡（当前大小与文件数、清理缓存一键清空、500 MB / 1 GB / 2 GB / 5 GB 上限、退出时清理开关默认关）。验证：缓存测试全过（复用、mtime 失效重下、LRU 淘汰、对账、清理）；设置往返测试覆盖新字段。**诚实缺口：`zydrive-preview://` 在真实 webview 里能不能加载（`<img>`/`<video>`/`<iframe>` 吃不吃 Go 注册的自定义 scheme 子资源），本机 Linux VM 没有 webview、dev-shell 也没有 mygo 运行时，验证不了——Mac 上必须实测；如果 webview 不认，退路是 127.0.0.1 HTTP 代理（混合内容策略要另验）。系统打开那半（`qlmanage -p`/`open`）也只在 Mac 上能验，下载那半有测试覆盖。**

顺手修的两个前端问题（都是测试先发现的）：① 视频/音频播不出来时，“流式播放中”的提示还挂着——现失败态不显示该提示；音频失败时原来连降级文案都没有，现与视频一致（文案 + 系统打开按钮）。② `TestSettingsRoundTripAcrossRestart` 旧测试的期望值没跟上新字段：它期望重启后 `PreviewCacheLimitMB` 为 0，但 0 不是合法上限（保存时转默认 2048）——现把新字段显式填进期望值（1024 + 退出清理开），让测试真正覆盖往返。

visual-check 本轮还修了两个测试本身的缺陷（UI 是对的，截图为证）：① `hasText: 'Box'` 子串匹配到了 Dropbox 卡（Playwright hasText 不区分大小写）——改锚定正则点名字元素；② “一键授权还没配置”只在选中一键卡时才渲染，Box/pCloud 默认选“自己的应用”——改断言灰卡副标题“这个网盘的官方应用还没注册好”。另：桩里不能出现无法解析的 URL——视频/PDF 若给 `zydrive-preview://`，headless Chromium 的鼠标输入会被卡死（单开视频或单开 PDF 没事，两个都开过再关 Inspector，连侧栏导航都点不动，刷新页面也恢复不了；DOM 点击能导航，说明应用没坏，是鼠标输入本身被卡住）——现桩里视频/音频/PDF 全部用 data-URL（视频是无效字节、照样走降级分支）。这只是桩环境的怪癖，真机上 scheme 已注册。

v0.9.1 之后仍未做的诚实缺口：真实账号端到端仍只测过 S3/R2（新增的 Box/pCloud/MEGA 与所有一键授权流程全是 mock）；pCloud 欧洲区换 Token 端点待真实欧洲账号验证；断点续传只有 Drime；`zydrive-preview://` 与 Quick Look/system-open 需 Mac 实测；同步冲突仍只有 keep-both 且静默；空源目录不同步；检查更新未接；开机启动需打包后真机验证；英文只覆盖导航/顶栏/设置页。

## 附：v0.9.2 补丁（2026-11-26，用户在 Mac 上测 v0.9.1 时截图反馈的三处）

1. **测试通过变绿**：密钥类表单（MEGA 等）的连接测试通过后，状态条原来是灰底 notice-box，现改成绿底 success-box（边框 #86efac / 底 #f0fdf4 / 字 #166534，与 error-box 同一套路）；OAuth 授权组件里通过态也同步改绿。
2. **凭证说明改问号悬浮提示**：原来单独占一行的灰底长条“凭证只保存在你这台 Mac 上（加密存储），不会上传到任何地方。”已删，改成“已通过测试，点保存即存为已验证。”文末的“?”圆圈图标，鼠标悬停或键盘聚焦时在图标上方弹出气泡显示这段文字。OAuth 表单那边的同一段文字先没动——那边没有“已通过测试”这行字，问号没地方挂，等用户说再改。
3. **输入框焦点绿环左侧被裁**：根因是 `.dialog-flex .dialog-body` 只留了右侧 4px 内边距、左侧 0，`overflow-y: auto` 的滚动容器把 2px 焦点 outline 左侧裁掉了，右侧靠 4px 内边距保住了——左右不对称，看起来就是左侧绿被挡住。改成四边 4px 内边距（select 的 outline-offset 1px 也需要 3px 空间），根治。
4. **Dropbox 一键授权后不自动换 Token（2026-11-26，用户报：浏览器显示“授权成功”白页面，手动换取能成功）**：根因是过期闭包——轮询 interval 在 genAuthLink 里创建，那时 setLinkClientId 还没生效，轮询回调里 exchangeCode 读到的 linkClientId 还是 ''，自动换取拿空 Client ID 去换，Dropbox 拒了；手动点“换取 Token”读的是已更新的 state，所以能成功。和 2026-11-25 修的 oauthState 过期闭包同一类 bug，那次只修了 state，漏了 linkClientId/linkClientSecret。修法同款：加 linkClientIdRef/linkClientSecretRef，genAuthLink 里同步写 ref，exchangeCode 优先读 ref。验证方法：先让检查桩的 ExchangeOAuthCode 像真 Dropbox 一样拒空 client_id，跑出复现（自动换取 client_id 为 ''、无 Token）；修完再跑，自动换取 client_id 为 'zydrive-builtin-id'、Token 与自动测试都通过，检查里加了该断言，防回归。

## 附：v0.9.3 补丁（2026-11-26，MEGA 挂载加载失败的诊断与恢复；已推，提交 b3892ed9）

1. **故障**：用户在 Mac 上加好的两个 MEGA 盘（mega001、mega002）在 0.9.2 更新重启后加载失败，侧栏显示“出错 / 容量暂不可用”，文件浏览红字 `app: mount "…" 未能加载（凭证或配置有问题），请删除后重新添加`。该报错来自 `ProviderFor` 的 nil 驱动分支，真实原因被 `LoadMounts` 吞掉了（`Registry.New` 失败只置状态、不记日志、不留错误），外面无从得知。查证：0.9.1 与 0.9.2 的后端 Go 代码除版本号外完全相同（GitHub 对照过），所以不是 0.9.2 改坏的，是重启暴露了潜伏的恢复失败；MEGA 是唯一在构造时就发网络请求（会话登录 → `f` 命令）的驱动，OAuth 类驱动都是离线构造、首次使用才联网，所以启动期一次瞬时失败就会永久杀死 MEGA 盘、别的盘却能自愈。
2. **修法（四件）**：① `storage.ErrSessionExpired` 哨兵错误；② MEGA 适配器对会话登录失败做分类——服务器明确拒绝会话（`f` 命令回错误号，如 `[-9]`，解码成类型错误）才包 `ErrSessionExpired`，瞬时失败（net/url 错误、JSON 语法错误、EBADRESP）原样返回、不标过期——关键点：go-mega 的 `api_request` 会吞掉传输错误（循环内 `err` 遮住了具名返回值，返回空 body + nil 错误），于是传输失败在 `getFileSystem` 里表现为“unexpected end of JSON input”，仍是“服务器没回答”，不是“会话被拒”；③ `LoadMounts` 把构造失败的真实原因记进日志，并把 `ErrSessionExpired` 映射成 `MountAuthExpired`（前端早已会渲染成“授权过期了，重新授权后就能继续浏览。”）；④ 文件浏览错误框的“重试”改走 `TestMount`（按保存的凭证重建驱动，失败返回真实原因），原来只调 `load()` 重新列目录、驱动是 nil 时永远得到同一个错；`ProviderFor` 的文案也按状态区分（过期 → 指去设置重新授权；其他 → 先重试）；`friendlyError` 加 `session expired` 映射。另加 `newWithClient(cfg, client)` 注入点供测试。
3. **真账号复现的意外结果**：用户给了 mega001 的账号密码（只用一次、环境变量传参、未落盘、复现用测试文件已删），本 VM 的 Go 客户端能连 MEGA（curl 超时是沙箱拦 curl，不是连不上）。结果：密码登录在第一步就被 MEGA 拒绝，错误是 -16 `User blocked`（EBLOCKED），不是密码错（密码错是 -9），也不是会话恢复那一步——连会话都没拿到，无从验证会话恢复。用户 2026-11-26 10:32 确认：账号被锁，重置密码后恢复；挂载也随之恢复且无需重新授权——ZyDrive 存的是会话不是密码，锁定时会话也被拒、解锁后旧会话继续有效（这次密码重置没有让旧会话失效）。根因确认：账号被锁 → 会话被拒 → 启动加载失败，非代码 bug。
4. **验证**：go vet + go test 全过（21 个包；全量跑时 `TestRunnerRetryOnce` 一次因时序失败，单独重跑 3/3 通过，与本次改动无关，agentrun 未动）；新增适配器假服务器测试 3 个（拒绝 → ErrSessionExpired、有效会话恢复成功、网络失败不标过期）与应用层重启往返测试 2 个（MEGA 盘重启后状态 OK、会话失败标 auth_expired 且报错指去重新授权）；npm 构建 JS 418.27 kB；make build 三二进制均报 0.9.3；visual-check 三种模式全过。

## 附：文件去重设计（2026-11-25，用户：各云去重逻辑不同，可能需要给每个云单独做）

### 什么算重复（两档）

- **确定重复**：内容哈希相同 + 大小相同。
- **疑似重复**：大小 + 名字相同，但哈希对不上或没有哈希——只提示“疑似”，让人自己确认，不能直接判死刑。
- 同名但内容不同不算重复，要单独标出来，不能混进重复组里。

### 各云哈希口径（核心难点：算法不一样，没法直接比）

| 云 | 能拿到的哈希 | 能不能跨云比 |
|---|---|---|
| Dropbox | `content_hash`（4 MB 分块各算 SHA-256，再拼起来算一次 SHA-256） | 只可与 Dropbox 自身比 |
| Google Drive | `md5Checksum`（内容明文 MD5） | 只可与 Drive 自身比 |
| OneDrive | `quickXorHash`（主）、`sha1Hash` / `sha256Hash`（有时有） | 只可与 OneDrive 自身比 |
| S3 / R2 | ETag：单段上传 = MD5；分片上传 = 各段 MD5 再拼的 MD5 + 段数，不可当纯 MD5 比 | 基本不可跨云比 |
| Drime | `hash` 是 URL-safe ID，不是内容哈希；`file_hash` 只有客户端声明的 SHA-256（64 位 hex）或 MD5（32 位 hex）才可信 | 基本没有可用哈希 |
| 本地文件夹 | 直接算 | 以我们的 SHA-256 为准 |

### 策略

- **云内**：用这朵云自己的哈希比（Drive 的 MD5 比 Drive 的 MD5，没问题）。
- **跨云**：哈希比不了，退到大小 + 名字给“疑似”；或下载下来流式算哈希（边下边算，算完就扔，不存文件）——这是深度去重，要用户主动点，不能静默跑。
- **我们自己上传的**：上传时顺手把 SHA-256 记进本地索引（与客户端加密的本地索引同一套机制），以后比我们自己的哈希，不用下载。别人传上来的旧文件没有这个哈希，走上面两条路。

### 流程与安全规则

扫描 → 按哈希 / 大小+名分组 → 预览列表（每组列出重复文件：路径、大小、修改时间，用户选留哪一个）→ 批量删。

- 不自动删，必须用户预览后二次确认；每组至少留一份。
- 删进各云的回收站（Drime 默认进回收站，Dropbox、Drive 也有），删错了还能捞回来。
- 正在同步任务范围内的文件，删之前要提醒。

### 加密文件去重

密文哈希没用：同样明文每次加密用的 nonce 和内容密钥都不同，密文每次都不一样。必须用本地索引里的明文哈希去比（见 `docs/client-encryption.md` 第八节）。

### 结论

各云去重逻辑不同，很可能要每云单独做：哈希怎么拿、拿什么哈希、怎么比、删了进不进回收站、回收站语义是什么，每家都不一样。不要先写一套通用比较器再逐个打补丁，直接按云拆实现，共享的只有分组、预览、二次确认这套流程壳。

## 附：预览缓存管理规则（2026-11-25，用户：设置里要有清理缓存，后台缓存管理要明确）

### 缓存是什么

只存“系统应用打开”那档预览下载下来的文件副本。应用内直接预览（图片、视频流式、PDF、文本）不落盘，不进缓存。缓存里的文件永远是云端原文件的副本，删了随时能重新下载，删缓存不碰云端。

### 位置与键

- 位置：各平台应用缓存目录（macOS `~/Library/Caches/com.luckzk.zy-drive/preview`，Windows `%LOCALAPPDATA%/zy-drive/preview`，Linux `~/.cache/zy-drive/preview`）。
- 键：mount_id + 远端路径哈希，避免不同网盘同名冲突。
- 清单：SQLite 一张表记每条缓存（键、大小、远端 mtime/size、最后访问时间）；启动时清单与磁盘对账（文件没了就删清单条目，磁盘上有清单外的孤儿文件就删文件）。

### 大小与淘汰

- 默认上限 2 GB，设置里可改（500 MB / 1 GB / 2 GB / 5 GB）。
- 写入时超上限 → 按最后访问时间（LRU）淘汰，直到低于上限；最近 5 分钟内访问过的文件不淘汰（正在系统应用里打开的文件不会被删）。
- 改小上限、启动时，都做一次裁剪到上限。

### 只读

缓存文件写完后置为只读（0444），系统应用打开即为只读——避免用户在预览副本里改了东西，以为能同步回云端。

### 写入与复用

- 原子写入：先写临时文件，写完 rename，半截下载永远不会被当成完整缓存。
- 复用：同一 mount+路径已缓存且大小与远端 mtime 一致 → 直接用，不重新下载；远端变了就重新下载覆盖。

### 清理

- 设置 → 缓存：显示当前大小与文件数；“清理缓存”一键清空（云端有原件，无数据丢失风险，不用二次确认），清理后提示释放了多少。
- “退出时清理缓存”开关，默认关。
- 缓存事件（淘汰、清理）只记 debug 日志，不打扰用户。

### 不属于清理缓存范围的

挂载元数据、同步任务、本地解密索引（SQLite）——体量小、自动管理，不进设置里的清理入口。

## 附：v0.10.0（2026-11-26，拖拽分组 + 右键菜单；未推，等用户确认）

用户 2026-11-26 11:39 定：“两个一起做，但是拖拽分组这个是长按就能拖拽”。原型 Demo v8 画的是握柄拖拽（`.mdrag`），用户明确改成长按，握柄没做。

### A. 长按拖拽换分组（纯前端，Sidebar.tsx）

- 按住网盘卡片 400ms 且移动不超过 6px 才进入拖拽态；按住后先移动 = 普通点击，不触发拖拽。普通单击仍选中网盘；悬停的移除（垃圾桶）按钮照常工作，按钮区域的 pointerdown 不启动拖拽。
- 拖拽中：卡片半透明 + 阴影（提起态），所有分组行（含“未分组”）成放置目标，悬停的分组按原型样式高亮：2px 品牌绿虚线框、outline-offset 2px（`.group-drop-target`，对标原型 `.mgroup.dragover`）。
- 松手落在某分组 → `api.updateMountGroup(mount.id, 分组名)`（未分组传 `''`），成功后 `onGroupsChanged()` 刷新；落在别处或按 Esc 取消；拖拽结束后的 click 用标记拦截，不误触发选中。指针事件自实现，不用原生 draggable（原生做不了长按启动）；拖拽中禁文本选中；长按期间禁浏览器原生菜单。
- 测试教训（检查桩里）：分组按名字 Unicode 排序，“个人”排在最前——拿侧栏第一张卡当拖拽源会拖到同组、变成 no-op，断言不到更新；改成拖“工作”组第一张卡（我的 R2）。另：“工作”组在侧栏可视区之外时要先 `scrollIntoViewIfNeeded()`，否则按住点落在视口外、拖拽起不来。

### B. 文件右键菜单 + 三个新后端方法

- 后端（`internal/app/fileops.go` 新文件）：`DeleteNodes`（逐个调 Provider.Delete，逐项结果；Provider 为 nil 时返回 v0.9.3 的人类可读错误；Caps.CanDelete 为 false 直接报错）、`RenameNode`（空名、含 `/` 或 `\`、`.`/`..`、改根目录都拒绝；同名是 no-op；目标 ref = 同目录 + 新名）、`MoveNodes`（同盘逐个 Rename；跨盘先走 BatchCopy 排队，复制成功的源文件再由 worker 逐个删——实现方式是在 Transfer 上加 `DeleteSource` 标志（migration v23，`transfers.delete_source`），`executeCopy` 在 `dst.Upload` 成功后才删源，复制失败的源文件绝不删；空文件夹没有文件可复制，改为同步在目标建目录 + 删源文件夹，否则移动空文件夹会静默无事发生）。三个方法都绑进 DesktopService（bind.go），`frontend/src/mygo.ts` 重新生成，`api/client.ts` 加 wrapper。
- 菜单项按 Caps 显隐（不支持的能力不出现，不灰掉点不动）：单选——打开（文件夹进目录；文件选中并开 Inspector）、预览、下载（现有 batchDownload）、复制到…（现有 UploadTargetDialog + batchCopy）、移动到…（复用 UploadTargetDialog + 新 MoveNodes）、重命名（仅单选）、删除（二次确认）、在 Finder 中显示（按系统换文案，与 PreviewPane 共用 `revealLabel`）、用系统应用打开、详情（选中并开 Inspector 且高级页默认展开——Inspector 用 nonce 信号 `openAdvancedNonce` 展开，不重挂载、不在点别的行时误展开）；多选——下载、复制到…、移动到…、删除（无重命名）。
- 删除语义（按 Caps.DeleteToTrash）：能进回收站的盘（Box/Drime/Drive/Dropbox/MEGA/OneDrive/pCloud）写“将移入回收站，可在网盘回收站恢复。”；本地文件夹、S3/R2 写“将永久删除，无法恢复。”。确认框显示数量 + 前几个文件名；危险按钮确认后才执行。删除/移动/重命名后刷新列表，按现有 batchMsg/batchError 模式显示结果，清空相应选择。批量栏（`checked.size>0` 的 notice-box）也加了一个“删除”按钮，走同一确认与删除流程，样式与原型一致用红色（`.btn-danger`）。
- 跨盘移动收尾（前端轮询，复用上传轮询的 2s 间隔与 30 分钟上限）：等队列里的移动全部终态后，文件夹下所有文件都已复制成功、且没有失败的，才删源文件夹——有任何失败文件留在里面时绝不删；队列里找不到的传输视为已终态（不阻塞轮询），但它的文件夹不参与清理。嵌套空子文件夹不在此列：BatchCopy 本来就不复制空目录，移过去的文件夹里空子文件夹会丢（已知缺口，见下）。
- 另：批量下载/复制改成显式传 refs（右键菜单可能只作用于单行，不在已选集合里；只清掉实际操作过的选择）；UploadTargetDialog 加 `verb` 参数，移动框文案是“移动”不是“复制”。

### 顺手修的共享样式真 bug（`.dialog-actions` sticky 底栏盖字）

- 现象：删除确认框里“将移入回收站，可在网盘回收站恢复。”只剩上半截字，下半截被一条横线拦腰截断。查了三轮才定根因：DOM 排版完全正常（Range 矩形 392–410、盒子 391–411、无元素盖住、动画已结束、无溢出），像素数出来墨迹在 y=402 处干净截断；最后量到 `.dialog-actions` 的实测顶边是 403.19——它是 `position: sticky; bottom: 0`，短对话框不滚动时粘性条被拉到对话框底部内边处、盖住了上面那行字的下半截（实色背景 + 顶边框就是截图里那条横线）。tokens.css 自己的注释还写着“短对话框不滚动，所以粘性是不起作用的”——实测证明这句话是错的。受影响的不只删除框：所有短对话框（重命名框同理）都会被盖；长对话框（UploadTargetDialog 长列表）本来就靠 sticky 让底栏滚动时可见。
- 修法：`.dialog-actions` 拿掉 `position: sticky; bottom: 0`；UploadTargetDialog 改走 AddMountDialog 已在用的 `.dialog-flex` 模式（标题固定、中间 `.dialog-body` 滚动、底栏静态），长列表底栏依然固定、短内容底栏在正常流里不再盖字。检查里加了永久回归断言（底栏顶边必须在文案行底边之下）。

### Demo v8 对比结论

- 对齐：分组 dragover 绿虚线（2px  dashed #56E16C、offset 2px，与原型 `.mgroup.dragover` 一致，多一个 12px 圆角）；批量栏四项齐全（已选 N 个 + 下载 + 复制到… + 删除（红）+ 取消选择，与原型一致，仅顺序不同——原型复制在前、下载在后，现有顺序是下载在前，v0.10.0 之前就这样，没动）；复制/移动目标框复用同一组件（原型是专用的“复制到其他网盘”弹窗带计划预览，按任务要求复用 UploadTargetDialog，未按原型另做）。
- 有意偏离（用户/任务已定）：拖拽是长按启动，不是原型的握柄 `.mdrag`（用户 2026-11-26 11:39 亲定）；右键菜单“详情”直接开 Inspector 高级页，原型是设置页里一个开关控制右键菜单是否显示“高级”入口（v0.10.0 没做那个开关）。
- 未做（既有缺口，不属 v0.10.0）：Inspector 里没有原型那三个操作按钮（打开/下载/复制到…），Inspector 一直只有详情展示 + 高级手风琴，这些操作目前只在右键菜单与批量栏里。

### 验证（2026-11-26）

- go vet 全过；go test ./... 全过（21 个包；新增 `TestMoveNodesCrossMountEmptyFolder`，fileops 测试共 8 个）；npm 构建 JS 428.36 kB / gzip 132.32 kB；make build 三二进制均报 0.10.0；visual-check 三种模式全过（默认 / VISUAL_MOUNTS=1 / VISUAL_MOUNTS=1 + VISUAL_SLOW_LOAD=1），新增 2h（右键菜单：菜单含复制到…/移动到…/重命名/删除；删除确认框回收站文案；点取消菜单关闭；移动到…打开目标框）与 2i（长按 450ms 进拖拽态、拖到“个人”组后 UpdateMountGroup 被调到 `["m-s3", "个人"]`、卡片换组）断言段，桩补了 UpdateMountGroup/DeleteNodes/RenameNode/MoveNodes。
- 诚实缺口：真机长按手感（400ms 是否顺手、误触率）需用户 Mac 实测；跨盘移动大文件耗时与收尾轮询（2s 间隔、30 分钟上限）在真机大文件上未验证；嵌套空子文件夹跨盘移动会丢（BatchCopy 本来就不复制空目录）；重命名/删除/移动的真实账号端到端仍是 mock 契约测试 + 本地盘测试，未在 Dropbox/Drive 等真账号上跑过。

## 附：v0.11.0（2026-11-26，侧栏排序 + 拖拽丝滑化；未推，等用户确认）

用户 2026-11-26 13:08 反馈 v0.10.0 的拖拽“不是很丝滑”，并且要的不仅是换分组，还要改顺序：“比如我拖动 8，是可以放到 4 的位置，然后剩下的向下移动，有过渡动画。”

### A. 排序字段与 MoveMount（后端）

- 迁移 v24：mounts 表加 `sort_order INTEGER NOT NULL DEFAULT 0`；迁移 v25 回填：按 created_at 顺序给每组内现有盘依次编号（同 created_at 时按 id 排），现有顺序不变。两个迁移是因为框架每条迁移只执行一条 SQL。
- `ListMounts` 改成 `ORDER BY sort_order ASC, created_at ASC`。`sort_order` 只在组内有意义（每组内从 0 连续编号）；全局顺序由每组内的 sort_order 决定，前端按组过滤渲染。
- 新方法 `MoveMount(ctx, id, group string, index int) (*domain.Mount, error)`（`internal/app/accounts.go`，仿照 UpdateMountGroup 的内存 + DB 双写）：把一个盘移到目标分组内的第 index 个位置，index 夹取到 [0, 目标组盘数]。同组内移动时被挤开的盘依次后移（8→4 则原 4..7 变成 5..8）；跨组时源组收拢、目标组腾出位置；移到空分组即 index 0；移到同一位置是 no-op（不写库，UpdatedAt 不变）。DB 层 `UpdateMountOrders`（`internal/cache/mounts.go`）一个事务批量重编号，只写变了的行；`UpdateMountGroup` 改成追加到目标组末尾（同组是 no-op）；新增挂载默认追加到组末尾（sort_order = 组内 max+1）；删盘后同组内后面的盘收拢（保持 0..n-1 连续）。
- 绑进 DesktopService（bind.go），`frontend/src/mygo.ts` 重新生成，`frontend/src/api/client.ts` 加 `moveMount` wrapper。Go 测试（`internal/app/mountorder_test.go`、`internal/cache/migrate_test.go`）：同组 8→4 顺序与后移、跨组两组都对、空分组、index 越界夹取、同位 no-op 不改 updated_at、重启后顺序保持、删盘收拢、新盘追加到末尾、v23 库升级回填。

### B. 可排序的拖拽（前端，Sidebar.tsx 重写）

- 触发不变：按住卡片 400ms 且移动 ≤6px 才进拖拽态；单击仍选中；垃圾桶按钮区不启动拖拽；Esc/落空（同位松手）取消。长按等待期间卡片给轻微按下态（scale 0.98 + 阴影加深，`.mount-card.pressing`），等待不感觉死滞。
- 丝滑跟手：拖拽中原卡片留在原位占位（`visibility: hidden`，不触发布局重排），一个 `cloneNode(true)` 的克隆（`position: fixed`，`transition: none`）跟随指针；位置更新走 rAF 循环自续 + 直接改 DOM style，拖拽中不经过 React state——v0.10.0 每 mousemove 都 setState 是“不丝滑”最可能的根因。rAF 循环必须自续：只靠 pointermove 驱动的话，指针停在边缘等自动滚动时就没帧可跑、滚动会卡住（检查里实测复现过一次）。克隆提起来态（半透明 + 阴影 + 轻微放大），拖拽中禁文本选中。stagger 的 rise 动画会盖住 JS 直写的 transform：原卡片靠 `.sidebar.dragging` 关动画，克隆在 body 下、必须在 `.mount-card.dragging` 上直接写 `animation: none`。
- 插入位置指示：按指针 Y 在扁平化的卡片列表（各组卡片按渲染顺序，含组头区域）里算最近的插入缝隙；在该缝隙处画一条 2px 品牌绿插入线（`.insert-line`，fixed 定位，JS 直写 top/left/width）；插入点所在的分组即目标分组（线落在哪个分组的区域内就进哪个分组；空分组的组头下方可落点，index 0；“未分组”同样可落）。落点变化时插入线实时跟随。v0.10.0 的“拖到分组行换组”被位置插入统一替代，分组行绿虚线框高亮已去掉。
- 其余卡片下移过渡：被插入线挤开的卡片按插入点用 transform 平移让位（下移时插入点之后的卡片上移填原位，上移时插入点之前的卡片下移让位），走 `.mount-card` 已有的 transform 200ms 过渡（cubic-bezier(0.22,1,0.36,1)），不瞬间跳变；只有插入点变化时才改 transform，不每帧重写。侧栏滚动时几何重测（rect 是视口坐标）；指针贴近上下沿 48px 时自动滚动（每帧 12px），把上方/下方的分组带进视野。
- 提交：松手时克隆先用 200ms 动画落进最终位置（无变化时回原位），再乐观更新本地顺序（App 的 setMounts 经 `onMountsReordered` 传进来，Sidebar 内部按渲染顺序算好新数组；`flushSync` 同步提交，与清掉拖拽内联 transform 在同一任务内完成，中间没有一帧会画出“旧顺序 + 已清 transform”的闪跳），然后调 `api.moveMount(id, group, index)`；失败时 friendlyError 提示 + `onGroupsChanged()` 重拉回滚。
- 检查联动修（v0.11.0 暴露的顺序假设）：4b5 原地重新授权段原来点列表第一个“重新授权”按钮、默认它是 S3 网盘；2i/2j 拖拽改了顺序后第一行变成 Dropbox 网盘，对话框里没有桶名、填表超时。改成按名字找“我的 R2”那行点——顺序会变，测试不能再假设列表首行是谁。

### Demo v8 对比结论

- 对齐：侧栏结构（分组 + 数量徽标 + 卡片 + 容量条）、品牌绿 #56E16C、200ms cubic-bezier(0.22,1,0.36,1) 过渡、卡片提起态的阴影，与原型一致。
- 有意偏离（用户亲定）：原型拖拽是握柄式（`.mdrag` ⠿ + 原生 HTML5 draggable，`title="拖到其他分组可以移动"`），只能换分组、不能改组内顺序；v0.11.0 按用户 2026-11-26 13:08 的要求做长按启动 + 组内排序（插入线定位置、其余卡片下移让位），分组行绿虚线高亮（原型 `.mgroup.dragover`）被插入线替代，握柄没做（v0.10.0 已按用户 11:39 的决定去掉握柄）。

### 验证（2026-11-26）

- go vet 全过；go test ./... 全过（21 个包；`TestBatchDownloadLocalEndToEnd` 在整包并行跑时失败过一次——下载 .part 重命名报 no such file，单独重跑通过、整包重跑也通过，判定为并行负载下的 flake，与排序改动无关；已知 flaky 的 TestRunnerRetryOnce 这次没抖）；npm 构建 JS 434.07 kB / gzip 134.31 kB；make build 三二进制均报 0.11.0；visual-check 三种模式全过（默认 / VISUAL_MOUNTS=1 / VISUAL_MOUNTS=1 + VISUAL_SLOW_LOAD=1；挂载模式连跑两次都过），新增 2i 改写（长按 450ms 进拖拽态、插入线随指针移动、拖到“个人”组首后 MoveMount 被调到 `["m-s3", "个人", 0]`、卡片换组排组首）与 2j（组内排序：长按最后一张拖到组首，MoveMount 被调到 `["m-long", "个人", 0]`、组内顺序真的变了；拖拽中克隆 transform 随指针连续变化），桩补了 MoveMount（按渲染顺序重排，与乐观更新一致）。
- 诚实缺口：真机长按手感（400ms 是否顺手、误触率）与拖拽丝滑度仍需用户 Mac 实测；卡片高度不一致时（超长名、带/不带容量条）插入线对齐只在桩数据上看过，真实挂载组合未验证；自动滚动速度（12px/帧）是拍脑袋定的，没在真机上调过；排序的真实账号端到端仍是 mock 契约测试，未在真账号上跑过。

## 附：v0.12.0（2026-11-26，版本历史真实现 + Copier + OneDrive 409 修复 + 拖拽分组标题跟随；未推）

用户 2026-11-26 拍板三件事：Drive/S3/Dropbox 的版本功能真做（不是改 Caps）、S3/Dropbox 的服务端复制接 Copier 接口；另报两个 bug：OneDrive 传 135 MB 文件报 409 却实际传上去了、拖拽时分组名称不会移动。

### A. 版本历史真实现（Drive / S3 / Dropbox）

- Google Drive（`internal/storage/providers/drive/drive.go`）：`Versions` 走 revisions.list（分页，取 id/modifiedTime/size/md5Checksum），版本节点带 `Extra["versionId"]`（与 OneDrive 同一约定）。`Restore` 是下载该 revision（revisions.get alt=media）再重传回同一文件、成为新 head 版本——Drive API v3 的 revisions 资源只有 get/list/update/delete，**没有服务端恢复端点**，已在代码注释里写明。Google 原生文档类文件（Docs/Sheets/Slides）没有可下载的 revision 字节，恢复会直接报错。
- S3/R2（`internal/storage/providers/s3/s3.go`）：`Versions` 先 `GetBucketVersioning` 再 `ListObjectVersions`（按精确 key 过滤，跳过删除标记）；**桶没开版本控制时返回空列表、不报错**（代码注释与 Caps 注释都写明了）。`Restore` 是 `GetObject`（带 VersionId）再经 transfermanager 重传为新 head。`Rename` 内部的复制步骤改走新的 `Copy`。
- Dropbox（`internal/storage/providers/dropbox/dropbox.go`）：`Versions` 走 `/2/files/list_revisions`，`Restore` 走 `/2/files/restore`（path + rev）；`dbxEntry` 加了 `Rev` 字段。
- 契约测试（`internal/storage/contracttest/contract.go`）：新增 `AssertCapsMatchInterfaces`——`SupportsVersions ⇔ Versioner`、`SupportsServerSideCopy ⇔ Copier`，从 `Run()` 里调用（跑在每家驱动的 mock/fake 上），另有 `TestCapsMatchInterfacesAllDrivers`（8 家无网络驱动用占位凭证构造；MEGA 因 `New` 要联网登录被排除，改由 fake client 的 `Run` 覆盖）。本次只断言 Versions + ServerSideCopy 两项，其余六个可选接口未断言。
- UI 接线：新 `internal/app/versions.go`（`ListVersions`/`RestoreVersion`，不支持的驱动报“不支持历史版本”）→ DesktopService 绑定 → `frontend/src/mygo.ts` 重新生成 → `client.ts` 加 `api.listVersions/restoreVersion`。Inspector“高级”里“查看历史版本”现在是真按钮（仅 `caps.supportsVersions` 且选中文件时显示），列出时间/大小，两步确认恢复，恢复后刷新列表与浏览视图；文件右键菜单加“历史版本…”（同一 Caps 门控），点开 Inspector 高级页并直接加载列表。没新开页面。

### B. Copier 服务端复制（S3 / Dropbox）

- `internal/storage/provider.go` 新增可选接口 `Copier`：`Copy(ctx, src, dst string) error`，同盘服务端复制，不删源。
- S3（`CopyObject`）与 Dropbox（copy_v2）实现；Caps 的 `SupportsServerSideCopy=true` 现在是真的。Drive 保持 false（files.copy 语义不同）。
- 接线（`internal/app/transfers.go` 的 `executeCopy`）：同盘 + 目标驱动实现 `Copier` 时走服务端复制（Move 的删源逻辑不变，仍在复制成功后删）；跨盘与无 Copier 的驱动保持原有流式 `OpenRead → Upload`。`BatchCopy` 注释同步更新。
- 顺手修了一个真 bug：`internal/transfer/worker.go` 的双重派发竞态——`Start` 在 goroutine 里才把状态翻成 Running，循环可能把同一个 Queued 任务再挑出来执行两次（Copier 测试里观察到 `copyCalls == 2`）；改成启动 goroutine 前先同步置为 Running。流式复制因幂等重传大多无感，服务端复制让重复变得可数。

### C. OneDrive 上传 409 修复（用户报的真 bug）

- 现象：135 MB .dmg 传 OneDrive，chunk PUT 报 409 `resourceModified`（eTag 不匹配）显示失败，但文件实际已传上去，最近同步里也显示成功。
- 根因：`putChunk` 把非 200/201/202/429/503 的状态一律当终局错误、不核实；最可能的机制是某块首次发送已落盘、响应丢了，重试再 PUT 同一 range 时服务器已持有这些字节（或文件已完整）而报 409；传输层又无自动重试，失败就立住了。
- 修法（`internal/storage/providers/onedrive/onedrive.go`）：409 `resourceModified` 不再直接失败，改走类型化 `*chunkConflictError`（`graphErrorCode` 解析；429/503 重试行为不动，409 绝不盲重试同一块）。`sessionUpload` 拆成 wrapper + `sessionUploadAttempt`（带重启计数），冲突时按顺序恢复：① 先核实（`verifyUploadComplete`）：Stat 目标文件，仅当整文件已读完（`bytesRead == size`，增量算出的 SHA-1 才完整）且服务端在大小、以及有 SHA-1 时在 SHA-1 上都对得上才算成功，返回节点并像正常成功路径一样更新 idMap——只对大小不足以通过（防同名旧文件误判）；② 续传（`queryUploadSession`）：GET 上传 Url 取 `nextExpectedRanges`，同一会话继续——整块已落盘就前进、只写了一部分就只补没写的尾巴（仍在 buffer 里）、一块都没落就重发一次；③ 会话已死（GET 404/410）：仅当 reader 是 `io.Seeker` 时建新会话从头重传，否则报清楚的错（不可从非 seekable 源恢复）。上限：每会话最多 3 次冲突恢复、每上传最多 1 次新会话重启。
- 另扩了一点（同类 bug 同一修法）：chunk PUT 本身返回 404/410 也走同样的核实优先恢复（丢失响应的重试可能撞上已完成并删掉的会话）。隔离在 `chunkSessionGoneError` 分支，若要收回只动那一处 + 一个测试。
- 回归测试（fake Graph 服务端）：最终块 409 且文件完整 → 成功（断言大小、SHA-1、字节内容）；最终块 404 → 成功；中途 409 → 续传完成；死会话 + seekable 源 → 新会话重传完成；死会话 + 非 seekable 源 → 明确报错。有效性验证：只回滚 `onedrive.go`（保留新测试）重跑，5 个新测试全失败、复现出上报的原错误，再恢复。

### D. 拖拽：分组标题跟随 + 丝滑化（Sidebar.tsx）

- 标题跟随：`applyInsertion` 现在同时平移分组标题行。标题按两套扁平坐标判断是否移动：`firstFlat`（该组首张卡在原始顺序里的位置——标题在被拖卡片之前还是之后）与 `rFirst`（去掉被拖卡片后首张剩余卡——标题在插入缝隙之前还是之后）；下移：标题在腾出的空位与插入点之间时上移一个卡片高度 + 间隙；上移反之；目标分组自己的标题不动、插入点之后的标题不动。`DragInfo` 新增 `headerShifts`，`clearShifts` 也清标题的 transform，松手/取消后不残留。
- 丝滑化（FLIP 式）：只有位移变了的元素才重写 transform，卡片与标题共用已有的 200ms `cubic-bezier(0.22,1,0.36,1)` transform 过渡，中途改目标时是滑过去、不是跳变；插入线的 `top` 也加了同一 200ms 过渡（首次落点用强制重排绕过，避免线从页面顶部扫进来），空分组的落线位置计入标题位移。
- 顺手修的滚动 bug：`measureGeometry` 在自动滚动时重测几何，但读的是已被位移的 rect，导致滚动中插入点算错；现在传入位移表并减掉，几何始终是未位移坐标。长按 400ms / ≤6px、fixed 克隆、rAF 自续循环、`api.moveMount` 落点流程没动。
- 检查新增 2k 段：长按“个人”组首张（超长名）拖到“存储 / 备份”组首，拖拽中断言“存储”标题上移了恰好一个卡高 + 间隙（实测 81px）、“工作”标题（插入点之后）没动，松手后 `MoveMount` 被调到 `["m-long", "存储 / 备份", 0]`。

### Demo v8 对比结论

- 本批无 Demo v8 原型对照项（原型无版本历史、无服务端复制概念、无上传冲突恢复；拖拽部分与 v0.11.0 一致，标题跟随是用户 2026-11-26 亲报的 bug）。

### 验证（2026-11-26）

- go vet 全过；go test ./... 全过（21 个包，一次过，无 flaky；新测试：OneDrive 409 五个、Drive/S3/Dropbox 版本恢复、S3 版本未开、Copier 同盘/回退、契约 Caps 断言、app 层版本方法）；npm 构建 JS 436.81 kB / gzip 135.07 kB；make build 三二进制均报 0.12.0；visual-check 三种模式全过（默认 / VISUAL_MOUNTS=1 / VISUAL_MOUNTS=1 + VISUAL_SLOW_LOAD=1），新增 2k（标题跟随：存储标题 682→601、恰好 −81px，工作标题不动）。
- 诚实缺口：新增的驱动行为全是 mock/fake 测试——Drive revisions、Dropbox list_revisions/restore、S3 ListObjectVersions（真实开版本控制的 R2 桶）、S3 经 transfermanager 的恢复、OneDrive 409 恢复，都还没在真实账号上跑过；Drive 恢复对 Google 原生文档类文件会失败（无可下载 revision）；S3 的 `Versions()` 每次列表多一次 `GetBucketVersioning` 调用，未开版本控制的桶上按钮仍显示（Caps 是静态 true），列表为空并提示“需先开启桶的版本控制”；版本 UI 只在构建层面验证过，没跑过浏览器检查（但 visual-check 三模式全过）；拖拽真机手感（长按、跟手、自动滚动速度、空分组进出）仍需用户 Mac 实测；2k 没覆盖拖进/拖出空分组。

## 附：v0.13.0（2026-11-26，文件浏览页 UI/UX 批；未推）

用户 2026-11-26 在 Mac 上截图反馈文件浏览页的六件事：命令面板先偏右再弹回中间、主面板容量与侧栏重复、顶栏“新建同步”放错地方、要列表/网格切换、详情页信息乱、"在 Finder 中显示"的提示要改成右上角 Toast。

### A. 命令面板居中修复（tokens.css）

- 根因：`.palette` 用 `left: 50% + transform: translateX(-50%)` 居中，同时挂了 `rise-sm` 入场动画；关键帧的 `transform` 在 500ms 动画期间整个替换掉居中的 `translateX(-50%)`，面板先出现在右侧、动画结束才弹回中间。
- 修法：改成 `left: 0; right: 0; margin-inline: auto` 居中，`transform` 留给动画专用。入场/退场动画本身没动。
- 顺带审计了其他 rise-sm 用户：`.dialog` 靠 `.dialog-backdrop` 的 flex 居中、`.dock-panel` 靠 right/bottom 定位、`.sidebar-menu`/`.context-menu` 靠 top/left 定位——都不靠 transform 居中，用 rise-sm 是安全的。审计结论写进了 tokens.css 的动画区注释。

### B. 主面板去重容量（FileBrowser.tsx / Sidebar.tsx）

- 删了主面板的 `.browse-quota` 整行（容量文字 + QuotaBar + “刷新容量”按钮）；侧栏卡片的容量显示不动。
- 容量刷新有了新家：侧栏卡片悬停时出现的操作列里加了一个“刷新容量”按钮（IconRefresh），点它重拉全部容量（与原来按钮同一批调用 `loadQuotas`）。删除按钮与刷新按钮共用一个绝对定位容器 `.mount-hover-actions`（悬停/聚焦才出现，平时隐藏；触屏常显），不再是两个各自定位的按钮；长按拖拽的排除条件从 `.mount-remove` 改成容器类名。
- 视觉检查 2e/2g 同步改成断言容器（display + absolute 定位 + 两个按钮的 SVG/title）。

### C. 顶栏去掉“新建同步”（App.tsx / i18n.ts）

- 删了顶栏的“新建同步”按钮与 `goCreateSync`（无他处引用）；`i18n.ts` 的 `newSync` 键一并删了。顶栏只剩 logo + 搜索框。
- 同步新建的入口在同步任务页：`pages/SyncJobs.tsx:789` 的“创建同步任务”按钮（visual-check 3b 覆盖，本轮三模式全过即它仍能用）。

### D. 列表/网格视图切换（FileBrowser.tsx / FileGrid.tsx / tokens.css）

- 用户贴了另一项目的 shadcn/Base UI 分段按钮片段问能不能用：结论是不能照搬——`rounded-button` / `transition-button` / `data-slot` / Base UI tooltip / `bg-accent` 等 token 在我们 tokens.css 里不存在。按我们自己的 Drime tokens 重写了 `.view-toggle` 分段控件，只复用了两枚 lucide 图标的 SVG 路径（layout-list / layout-grid，内联）。激活态照 Demo v8 `.seg button.on`（浅灰底 + `0 1px 3px rgba(0,38,29,.12)` 阴影）；28px 小按钮上的全局 hover 上浮（translateY(-1px)）关掉了，否则会抖。片段自带的尾部 info 按钮没加——没什么可说明的。
- 切换是真功能：列表 = 现有表格；网格 = 新 `FileGrid.tsx` 卡片（缩略图 `thumbUrl`、无缩略图时大号 FileTypeIcon + 名字 + 大小/修改时间）。两视图同一批数据、同一套 props（`listProps` 一个对象共用，不会漂移）：同一套选择、同一套右键菜单、同一套双击打开、同一套 checkbox；另给两边都补了 Enter 键（文件夹打开、文件选中；表格以前没有）。
- 选择存在 localStorage（`zydrive.view`，默认 list），刷新后保持。位置在文件工具栏右上角（刷新按钮旁）。

### E. 详情页重排（Inspector.tsx / tokens.css）

- 删了详情页的“网盘容量”区（`quota` prop 与 QuotaBar import 一并去掉）；详情不再显示容量。
- 信息从松散堆叠的 `<p>` 行改成定义列表（`.insp-kv`，照 Demo v8 `.kv`：标签左、值右、行间 1px 分隔线），分组 + 小节标题：**基本信息**（类型 / 大小（仅文件）/ 修改时间 / 路径 / 来源）、**校验值**（仅有 hash 时出现，hashKind 作标签）、预览（不动）、**高级**（折叠；“支持的能力”与“删除方式/路径最长”也改成 kv 行，版本列表与两步确认恢复逻辑不动）。
- 字段一个没少：唯一的变化是 mime 没值时“类型”行显示“文件”（以前不显示该行），文件夹显示“文件夹”。Demo v8 的 `.v` 是 550 字重，这里全站统一 400，不加粗。

### F. 右上角 Toast（Toasts.tsx / App.tsx / FileBrowser.tsx / PreviewPane.tsx）

- 新 `frontend/src/components/Toasts.tsx`：`ToastHost` 挂在 App 顶层（`pushToast`/`dismissToast`，最多三条、超了丢最旧的）。样式照 Demo v8 的 toast（深底白字、圆角、阴影），位置按用户要求放右上角（原型是底部居中）。
- 行为：一条 5s 自动消失；悬停暂停倒计时（记剩余时间，不是从头重算）、移走继续；X 立即关闭；多条上下叠放。host 本身 `pointer-events: none`，只有卡片接事件，不挡页面点击。
- 迁移了两处“已在文件管理器中打开”：FileBrowser 的右键菜单“在 Finder 中显示”（原 inline notice-box）与 PreviewPane 的 reveal 按钮（原 `openedMsg`）；其他 `batchMsg`（批量下载/复制结果）与“已用系统应用打开”仍走原 inline 提示，没动。

### G. 文件浏览功能区总整理

- 面包屑行现在是：路径 crumbs + 弹性空隙 + 视图切换 + 刷新，间距与按钮分组收紧；其余页面没动。顶栏只剩 logo + 搜索后，`headerBtnRight` 的溢出自查在无按钮时返回 null（检查本身已处理 null）。

### 验证（2026-11-26）

- go vet 全过；go test ./... 全过（21 个包，一次过，无 flaky——这次 `TestRunnerRetryOnce` 与 `TestBatchDownloadLocalEndToEnd` 都没抖）；make lint exit 0（gofmt + vet + golangci-lint 0 issues + tsc）；npm 构建 JS 441.46 kB / gzip 136.18 kB；make build 三二进制均报 0.13.0。
- visual-check 三种模式全过（默认 / VISUAL_MOUNTS=1 / VISUAL_MOUNTS=1 + VISUAL_SLOW_LOAD=1），新增 2l（面板首帧中心 640 = 落位中心 640，无横向跳动）、2m（网格 12 张卡、刷新后仍是网格、切回列表）、2n（Toast 右缘 1264/视口 1280、top 16、悬停 4.5s 不消失、移走后自动消失）。2e/2g 按新容器改过。截图 02l/02m/02n/02e 已目视：面板居中、网格卡片与选中态正常、Toast 深底白字在右上角、悬停列是刷新在上、删除在下。
- 诚实缺口：网格视图的缩略图只在 `thumbUrl` 有值时显示——桩数据里 12 个文件都没有 thumbUrl，所以网格里全是大图标，真机上缩略图是否出现取决于驱动是否回 thumbUrl（目前只有支持 Thumbnail 的驱动会回）；Toast 的悬停暂停在真机上依赖鼠标事件，与 5s 计时器的竞态只在桩里测过；详情页“类型”行在 mime 缺失时显示“文件”是兜底文案，不是后端给的类型；列表/网格切换的键盘焦点行为（Tab 到卡片/行、Enter 打开）在桩浏览器里没逐项跑过，只在代码层面与表格对齐。
- 全屏预览 overlay（调研已定、用户已批“按你推荐的做”）不在本版：它是 Inspector 留一瞥 + 全屏覆盖层的大改动，与本批的详情页重排同文件、且 v0.12.0 未推——下一版（暂定 v0.14.0）再做，避免两次未推送的改动混进同一次推送。

## 附：v0.14.0（2026-11-26，全屏预览 overlay P1；未推）

用户 2026-11-26 14:50 说预览 UI 做得太差，调研（`~/workspace/research_notes/preview-ui-20261126/report.md`，§四）结论是：认真看文件应该是全屏覆盖层，侧栏只负责一瞥；把预览塞进 300px Inspector 是形态错位。用户 15:02 批了“按你推荐的做”。本版只做报告 §四的 P1（1–10 项），P2 全部不做。

### A. Overlay 壳（PreviewOverlay.tsx / tokens.css）

- 新组件 `PreviewOverlay.tsx`：fixed 盖满窗口，深色遮罩 `rgba(17,17,17,0.92)` + 白字 + 品牌绿点缀（信息按钮激活态、链接）；开/关淡入淡出 150ms（`useClosing`）；Esc 关；点遮罩空白处关（viewer 各自判断点在自身根上才关，点图片/视频本身不关——文字区点选不关，避免选文字时误关）；焦点陷阱（Tab 在 overlay 内循环，关后焦点回到原处）；打开时记住 `window.scrollY`，关闭时恢复。
- 层级：overlay z-index 70——高于对话框（60），低于右上角 Toast（80），overlay 里点的下载确认 Toast 仍看得见。
- 顶栏：左信息开关（i）、中间文件名（超长省略）、右下载 / 用系统应用打开 / 关闭 ×。**没有分享按钮**：应用层根本没有 Share 接线（provider-todo §0.2 仍 open），一个点不动的分享按钮是假 UI；等分享真接线了再加（见本附录末尾“未做”）。
- 入口四处：文件双击、列表聚焦时 Space、右键菜单“预览”、Inspector glance 下的“全屏查看”按钮。Space 与多选不冲突：有选中集合时 Space 仍开预览（Finder 行为）；焦点在 checkbox 上时 Space 仍是勾选（事件目标是 input 时跳过）。

### B. 逐文件切换（PreviewOverlay.tsx / FileBrowser.tsx）

- 左右边缘箭头（悬停才出现，端点处置灰、不循环——循环会丢位置）+ ← → 键；切换时先出骨架再出内容，没有白闪；切换后 Inspector 的选中也跟到新文件（`onNavigate` 同时 `setSelected`）。
- 切换范围是当前目录的文件（不含文件夹），按列表顺序；目录刷新导致下标失效时 overlay 不渲染（FileBrowser 的渲染守卫）。
- 每切一个文件就重置一遍 viewer 状态（图片缩放/旋转、字号、倍速、PDF 页码），不跨文件泄漏。

### C. 底部自适应工具条（PreviewOverlay.tsx / tokens.css）

- 按类型换内容：图片（缩小/缩放标签/放大/适应窗口/旋转）、PDF（上下页/页码输入/缩放/适应宽度）、视频/音频（倍速 0.5x–2x + 视频全屏）、文本/Markdown（字号 −/+/复制全文）。
- 半透明黑底药丸（`rgba(0,0,0,0.55)` + blur），鼠标静止 2s 后淡出、移动再出现；顶栏常显。
- 工具条与边缘箭头都放在 `.preview-content` 里——信息面板打开时它们跟的是浏览区中心，不是整个主区中心。

### D. 图片查看器（PreviewViewers.tsx ImageViewer）

- 点按（无拖拽）在适应窗口 ↔ 100% 之间切换；放大后可拖拽平移；滚轮缩放（0.1–8，步进 ×1.25）；旋转 90°（旋转/回适应时清掉旧平移）；缩放从“适应”起步时以 viewer 报回的实际 fit 系数为锚点，不会跳到 100%。
- 大图先显示缩略图（`thumbUrl`，半透明 + 轻模糊），全图到了再换，没有白屏等。滚轮用原生非 passive 监听——React 的 wheel 是 passive，`preventDefault` 拦不住页面滚动。

### E. 视频/音频（PreviewViewers.tsx MediaViewer）

- 沿用 v0.9.1 的流式 + Range：原生 controls（进度/音量）+ 工具条倍速（`playbackRate`）+ 视频全屏按钮；音频上方是大号类型图标 + 文件名（光秃秃的 `<audio>` 是一条细带，没上下文）。
- 播不出来（桩的无效字节、或编码不支持）时走失败面板（原因 + 下载 + 系统打开），不假装在播。

### F. PDF：iframe 换成 PDF.js（PreviewViewers.tsx PdfViewer）

- `pdfjs-dist@5.4.296` + `?url` worker import（构建产物里 worker 是独立 asset `pdf.worker.min-*.mjs`）；默认适应宽度；DPR 上限 2 的 canvas 渲染；渲染任务可取消（切页/缩放时旧任务 `RenderingCancelledException` 直接忽略，不是失败）；页码/缩放状态走 imperative `PdfApi` handle，壳的工具条只调它。
- 加载失败 → 失败面板（原因 + 下载 + 系统打开）。harness 的教训：静态服务的 MIME 表本来没有 `.mjs`，module worker 因 MIME 被拒、PDF 永远渲染不出来——harness 的 MIME 表已补 `.mjs → text/javascript`（检查脚本的改动，不进应用代码）。Go 自身的 mime 表本来就有 `.mjs → text/javascript`，真机 mygo 资产服务不受影响。
- 依赖：`pdfjs-dist@5.4.296`。

### G. 文本/代码/Markdown（previewShared.ts / PreviewViewers.tsx TextViewer）

- 代码走 highlight.js `lib/common`（与 Box 同族；Go 在列）+ 行号 gutter（独立左列、与代码同行高、横向滚动时 sticky-left；**不**按行切分高亮 HTML——多行 span 会被切断）。highlight.js 主题不引整份 `github-dark.css`（会污染浅色 glance）：只把 github-dark 的 token 颜色抄到 `.preview-overlay .hljs` 作用域下；未知扩展名不猜语言（不用 `highlightAuto`，大文件慢且错），直接原文。
- Markdown 走 marked（`marked@18.3.0`）+ DOMPurify，替换 PreviewPane 里的手写 escape-first 渲染器（glance 与 overlay 共用同一份 `renderMarkdownSafe`）；链接统一加 `target="_blank" rel="noopener noreferrer"`——同窗口导航会把整个应用顶掉。glance 的 Markdown 顺带有了基础样式（`.preview-md`，浅色基底 + overlay 内深色变体）。
- 截断阈值保持 512 KiB（后端 PreviewText 的既有限制，本版不动后端；提示文案“已显示前 512 KB，下载查看全文。”——Box 用 192 KB，我们沿用已有的 512 KB，不另起一个数）。
- 依赖：`highlight.js@11.12.0`、`marked@18.3.0`、`dompurify@3.5.0`。

### H. 右侧信息面板（InspectorInfo.tsx / PreviewOverlay.tsx）

- 默认关闭，i 键/顶栏按钮开关；打开时内容区收窄。复用 Inspector 的同一份内容（`InspectorBasicInfo` + `InspectorAdvanced` 从 Inspector 里抽出来，两边同一份 markup）：基本信息、校验值、高级（能力、版本入口）。**没有容量**——v0.13.0 的删除决定仍然有效，overlay 里也不加。
- 深色变体：kv 标签/值、分区标题、muted 文案改白字系，分隔线改淡；按钮保持浅色底（可读）。

### I. 加载/失败态（PreviewOverlay.tsx / PreviewViewers.tsx）

- 每种类型都是 骨架 → 内容 → 失败（原因 + 下载 + 系统打开 + 可重试时加重试）。不支持的类型（system / video-unplayable）直接给文件图标 + 大小 + 两个按钮，永不空白。视频/音频播失败、图片打不开、PDF 加载失败都走同一块失败面板。
- 失败原因按类型区分（图片“可能已损坏”、媒体“可能是编码不支持”、PDF 加载失败、系统 tier 说明原因），不是同一句“加载失败”。

### J. 共享层（previewShared.ts / PreviewPane.tsx / Inspector.tsx）

- 新 `previewShared.ts`：tier 判定（与旧 PreviewPane 同一套后缀表）、`escapeHtml`、`renderMarkdownSafe`、`highlightCode` + `codeLanguage`、`usePreviewSource` hook（图片/视频/音频/PDF 走 `previewURL`，文本/Markdown 走 `previewText`，system/video-unplayable 不 fetch——**harness 里绝不能加载 `zydrive-preview://` URL**，桩浏览器解析不了这个 scheme，会卡死真实鼠标输入）。
- `PreviewPane.tsx` 改走共享层 + marked 渲染（手写渲染器退役）；修了一个重写时发现的 bug：system/video-unplayable 的 hook 状态是 `idle` 不是 `ready`，消息必须在 `(ready || idle)` 分支里渲染。
- `Inspector.tsx` 的 glance 下加“全屏查看”按钮（仅文件）。`FileTable`/`FileGrid` 加 `onPreview`：双击文件开 overlay（文件夹仍是双击进入）、Space 开 overlay（文件夹忽略、checkbox 上忽略、preventDefault 拦页面滚动）。
- `vite-env.d.ts` 新建：`?url` worker import 需要它，之前没有这个文件。

### 未做（P2 + 明确不做，留给以后）

- P2（报告 §四）：缩略图索引条、幻灯片模式、Office 在线预览、图片缩放后下载、视频字幕/音轨、预览内文件操作（重命名/移动/删除）、触屏手势。
- 不做：评论/标注、Markup 修图、自建 Office 渲染、overlay 里放同步/传输面板（右下角传输胶囊不动）。
- **分享按钮**：等 provider-todo §0.2 的 Share 接线落地后再加；现在加就是假 UI。
- 预览缓存规则没动（2 GB 上限、LRU、设置页清理）。

### 验证（2026-11-26）

- go vet 全过；go test ./... 全过（21 个包；整轮里 `internal/agentrun` 抖了一次——`TestRunnerRetryOnce`，单跑通过、整包重跑通过，是已知 flaky，不是本版改动）；make lint exit 0（gofmt + vet + golangci-lint 0 issues + tsc）；npm 构建通过（JS 463.73 kB / gzip 142.57 kB，v0.13.0 是 441.46 kB——多出的是 pdf.js 懒加载块 439.53 kB + highlight.js common 块 165 kB + marked，首屏主包只多了约 22 kB）；make build 三二进制均报 0.14.0。
- visual-check 三种模式全过（默认 / VISUAL_MOUNTS=1 / VISUAL_MOUNTS=1 + VISUAL_SLOW_LOAD=1），新增 2o（overlay 盖满 1280×800、遮罩 rgba(17,17,17,0.92)、标题“封面.png”、图片 naturalWidth 240）、2p（Esc 关闭）、2q（“下一个文件”→ 旅行.mp4、← 回封面.png）、2r（放大后标签“适应”→“125%”、图片 240 → 300px）、2s（PDF canvas 1248×1248、页码 1 / 1、无 iframe）、2t（代码 `.hljs-keyword` = "package"、行号 5 行、Markdown h1“说明”+ 2 项列表）。截图 02o/02r/02s/02t 已目视：遮罩、图片、PDF "Hello" 页、代码高亮与行号都正常。
- 构建时修的两个真问题：① overlay 重构脚本吃掉了“下一个文件”按钮的 `</button>`（tsc 报错定位）；② harness 的 MIME 表缺 `.mjs` 导致 PDF worker 起不来（见 F）。③ 桩列表多了 说明.md 后，同步多选“全选本页”断言从 12 改成 13（检查脚本的改动，不进应用代码）。
- 诚实缺口：PDF.js 的 `zydrive-preview://` 取数与 `?url` worker 在真机 mygo webview 里还没验过（harness 走 data: URL + 静态服务，与真机路径不同）；`navigator.clipboard` 在 mygo 自定义 scheme 下是否可用没验过（复制失败时会如实 toast，不静默）；图片缩放的宽高过渡是特意破例（transform/opacity 规则之外，只作用于单张 img、拖动时关掉）；视频/音频的倍速与全屏只在桩里点过，真机播放体验待用户验；overlay 的焦点陷阱与 Space/方向键只在桩浏览器里跑过，真机键盘行为待验；网格缩略图、Toast 悬停计时、Inspector "类型：文件"兜底等 v0.13.0 的缺口原样保留。

## 附：v0.15.0（2026-11-26，主页面 UI 审计 Top 5；未推）

用户 2026-11-26 20:34 让看主页面 UI 还能怎么优化；审计（`docs/main-page-ui-audit.md`，13 个区）列了 Top 5，用户 21:25 批了“Top 5 全做”。本版就是这五项 + 同区的 S 项。上传文件入口先做了 mygo 文件选择器调查（见 A），不是先写按钮再找实现。

### A. 上传文件：先调查 mygo 的选择器，再写按钮

- 结论：原生选择器走得通——`mygo.Dialog.Open`（mygo v0.10.0，`dialog.go:141-176`；macOS NSOpenPanel、Linux GTK chooser；选项 Title/Filters/Multiple/Directory/ShowHiddenFiles/CreateDirectories/Parent）。但它是 Go 侧 API，mygo-runtime 0.10.0 没有导出 dialog（`frontend/node_modules/mygo-runtime/dist/index.d.ts` 完整导出表查过，dist/index.js 里 grep dialog 无结果），所以前端不能直接调；做法是新绑一个 Go 方法 + `mygo generate` 重生成绑定。
- 落地：`internal/app/app.go` 加 `pickFiles` 字段；`internal/app/settings.go` 加 `SetPickFilesHook` + `PickFiles()`（hook 为 nil 时报诚实错误“文件选择器在当前运行方式下不可用”，不假装能选）；`internal/app/bind.go` 加 `DesktopService.PickFiles`；`cmd/desktop/mygo.go` 在 `SetLoginHooks` 旁边装 hook（`mygo.Dialog.Open`，`Parent: mygo.CallerWindow(ctx)`，`Title: "选择要上传的文件"`，`Multiple: true`）。`mygo generate` 后 `frontend/src/mygo.ts:895` 多了 `pickFiles` 绑定；`client.ts` 加 `api.pickFiles()`。前端“上传文件”按钮点它 → 拿到路径 → 复用已有的 `UploadTargetDialog`（选目标文件夹）→ 走已有的 `handleUpload`，不另起上传路径。
- 死胡同（都试过、都不走）：① `<input type="file">` 能弹出选择框，但 DOM File 对象不带本地路径，而 `UploadFiles` 要的是绝对路径字符串——要走它得另做一套“传字节”的后端，不值得；② File System Access API（`showOpenFilePicker`）在 mygo webview 里没有任何证据；③ 在 `internal/app` 里直接调 `mygo.Dialog` 会打破“应用层不依赖 mygo”的分层——用 hook 模式，`internal/app` 只认一个函数字段。
- 真机缺口：无头浏览器打不开 NSOpenPanel，harness 只能验接线（桩 `PickFiles` 返回一条假路径，断言“点了按钮 → 弹出目标框、显示 1 个文件将上传”）；**真的选文件 → 上传整条路必须在用户 Mac 上验**。

### B. 新建文件夹（FileBrowser.tsx）

- 面包屑行加“新建文件夹”按钮（Demo v8 的 actions 行没有这个按钮——是审计建议的新入口，不是原型照搬）。对话框复用重命名对话框的样式：名称输入框 + 目标文件夹副标题；校验：去首尾空格、禁 `/` 和 `\`、空名确定按钮禁用；目标路径用 `UploadTargetDialog` 同款拼接（`ref ? ref/name : name`）；成功后 `void load()` 刷新列表 + 提示“已新建文件夹"X"。”。Drive/Dropbox 的 mkdir 是幂等的，所以文案不声称“之前不存在”。
- 面包屑行同时加了计数“13 项 · 共 22.7 MB”（审计建议；文件数是当前目录的节点数，总字节只计文件不计文件夹——文件夹大小本来就没有）。

### C. 列表排序 + 全选（FileTable.tsx / FileBrowser.tsx）

- 三列可点：名称 / 大小 / 修改时间；同一列再点切换升降序，换列回到升序；表头按钮带 ↑/↓ 指示（`aria-label` 是“按名称排序”等）。文件夹永远在前、每组内再按 key 排；文件夹大小按 0 计（不编数字）；修改时间按时间戳排。
- 排序偏好按网盘存 `localStorage`（`zydrive.sort.<mountId>`，与 `zydrive.view` 同款约定），默认名称升序；损坏的存值回落到默认。列表、网格、overlay 前后切换共用同一份 `sortedNodes`——overlay 的“下一个文件”从此跟可见顺序走（v0.14.0 时是桩列表原顺序；harness 2q 的期望因此从“旅行.mp4”改成“合同.docx”，见“验证”）。
- 全选：表头复选框（Demo v8 `#checkAll`），只勾当前目录可见行；半选时 `indeterminate`（ref 回调设，不走 state）；勾选行带 `.checked` tint——淡蓝 `#eef4ff`，复用 DualPane 的 `.multiselected` 颜色（不是新发明一个蓝）；选中行是淡绿 `--selected`，两者能一眼分清（截图上像素核实过：勾选行 rgb(238,244,255)，选中行 rgb(240,253,244)）。
- 同区 S 项：大小列右对齐（Demo v8 `.num`）；修改时间改相对时间（今天 HH:MM / 昨天 HH:MM，其余绝对；`formatTime`，表格/网格/Inspector/传输共用同一份——桩数据全是 6 天前，所以 harness 里仍显示绝对时间，今天/昨天分支只在真机上出现）。

### D. Inspector 操作按钮（Inspector.tsx / FileBrowser.tsx）

- 文件名下方一排 `.insp-actions`：打开 / 下载 / 复制到…，全部复用右键菜单的同一批 handler，不另写逻辑。打开：文件夹进目录、文件开全屏 overlay；下载：走批量下载（单条）；复制到…：走已有的目标选择框；没有其他网盘时不渲染“复制到…”（不摆一个点不动的按钮）。
- 位置说明：任务原文写“在 glance 预览下方”，审计 §8.1 与 Demo v8 写“文件名下方、kv 信息上方”——实际布局顺序是 文件名 → kv 信息 → glance，两者不可能同时满足。按审计/Demo v8 做了（文件名下方、kv 上方），见 02w 截图。Demo v8 的 `.insp-actions` 是居中 + `padding:12px 16px 4px`；我们的是左对齐 + `margin-top:12px`——Inspector aside 本身已有 16px padding，再加 16px 横向 padding 是双倍留白；Demo 自己的窄屏变体（line 502）也是左对齐，所以这不是偷懒，是按容器做的适配。

### E. 批量栏上移 + 移动到…（FileBrowser.tsx / tokens.css）

- 批量栏从表格下方挪到表格上方（面包屑行下面），`position: sticky; top: 0` 随滚动固定在主区顶部；淡绿底（`--selected`），不再是灰色的 notice-box——操作栏和结果消息必须一眼分清。结果消息（“已把 N 个文件加入下载队列”之类）仍留在表格下方，不跟进栏里（选完即清，写在栏里会跟着消失）。
- 加“移动到…”：复用已有的移动目标选择框（右键菜单同款），不在栏里另起一套。其他批量动作原样保留（下载到默认文件夹 / 复制到… / 删除 / 取消选择）。
- 与 Demo v8 的差异：Demo 的 `.bulkbar` 不 sticky（本版按审计要求加了 sticky）；Demo 的栏是 `padding:8px 18px; gap:10px`，我们是 `margin:0 -16px; padding:8px 12px; gap:8px`（通栏 bleed，文字起点比表格首列左 4px——肉眼难辨，留作以后统一）；Demo 的栏没有“移动到…”（本版按审计要求加的）；Demo 的栏文案是“已选 0 项”，我们沿用 v0.9.0 以来的“已选 N 个”。

### F. 命令面板边输边搜 + 方向键（CommandPalette.tsx）

- 输入后 150ms 防抖即搜（不再等回车；回车仍可显式重搜）；旧请求的回包晚到时直接丢（`searchSeq` 守卫）；↑/↓ 移动高亮行（边界夹住、不循环）、回车打开高亮行、Esc 关闭；匹配文字用 `<mark>` 高亮（查询先转义，不当正则用）；每行右端显示 API 本来就返回的大小/日期（`formatSize · formatTime`；文件夹只显示日期）。
- 底部“关闭”按钮换成快捷键提示“↑↓ 选择 · 回车 打开 · Esc 关闭”。Demo v8 的 palette 是命令列表（新建同步/添加网盘/上传文件/切换网盘），没有文件搜索、没有方向键、没有高亮——本版这五项全是审计要求的新增，不是原型照搬；Demo 的命令入口（新建同步任务等）我们本来就没有，不补。
- 搜索范围仍是当前网盘；顶栏占位文案改诚实了：“在当前网盘搜索… (Ctrl+K)”（之前写“搜索全部网盘”是假承诺）。跨网盘搜索是单独的 L 项，不在本版。

### G. 同区 S 项（tokens.css / App.tsx / Sidebar.tsx / InspectorInfo.tsx / PreviewPane.tsx）

- `--selected` token：`:root` 里 `#f0fdf4`、深色 `#1e3a26`；导航激活、网盘卡片选中、网格卡片选中、双栏行选中、表格选中行全部改走它（之前导航 `#dcfce7` 与表格 `#f0fdf4` 各说各的）。与 Demo v8 的 `--green-soft`（`rgba(86,225,108,.14)` ≈ `#e7fbea`）略有色差——我们的是审计要求的单一共享 token，不再每处一个绿。
- 刷新按钮 busy 态：加载中禁用 + 文案变“刷新中…”。
- Inspector“高级”文案矛盾修了：之前是“分享链接”列在支持能力里、下面又写“尚未接入”——现在是“上面是该网盘支持的能力；生成分享链接等操作尚未接入，后续版本提供。”（前半句说网盘侧能力，后半句说 ZyDrive 接线状态，不再自相矛盾）。
- glance 预览高度统一 240px（PDF iframe 与 Markdown/文本区之前是 320px）。
- 侧栏悬停“刷新容量”改名“刷新全部容量”（它重拉的是全部网盘的容量，旧文案误导；harness 2e 的期望同步改了）。
- 同步任务导航角标：App 拉 `api.listSyncJobs()` 计数（已有的 endpoint，SyncJobsPage 同款用法），回到浏览页时重数；0 时不渲染——绝不编数字。Demo v8 的角标是写死的 3，我们的是真数。

### 审计 open questions 的拍板（用户可推翻）

- 排序范围：按网盘（`zydrive.sort.<mountId>`），不是全局。
- 密度：单档，不加切换。
- 网格卡片大小：固定，不加调节。
- Agent 离线 last-seen：不做（后端没有这个字段）。
- 搜索范围：当前网盘 + 诚实占位文案；跨网盘搜索是单独的 L 项。
- Inspector 按钮位置：文件名下方、kv 上方（见 D 的说明）。

### 验证（2026-11-26）

- `make lint` exit 0（gofmt + go vet + golangci-lint 0 issues + tsc）；`go test ./...` 全过（21 个包；整轮里 `internal/agentrun` 又抖了一次——`TestRunnerRetryOnce`，单跑通过、整包重跑通过，是已知 flaky，不是本版改动；这次首轮失败的测试名没截到，只看到包 FAIL，重跑全过）；npm 构建通过（JS 469.36 kB / gzip 144.53 kB，v0.14.0 是 463.73 kB——本版只加了 UI 逻辑，没有新依赖）；`make build` 三二进制均报 0.15.0。
- visual-check 三种模式全过（默认 / VISUAL_MOUNTS=1 / VISUAL_MOUNTS=1 + VISUAL_SLOW_LOAD=1），新增 2u（新建文件夹对话框：空名确定禁用 → 输名 → 对话框关闭 + “已新建文件夹”消息；上传文件 → 目标框“1 个文件将上传”）、2v（默认名称升序首行照片；大小升序首文件笔记.txt、降序首文件旅行.mp4；表头全选 13 行带 `.checked` + “已选 13 个”）、2w（Inspector 有 打开/下载/复制到…；点下载出现入队提示；点复制到…弹出目标框）、2x（批量栏在表格上方、淡绿底 `rgb(240,253,244)`、有“移动到…”并弹出目标框）、2y（输“行程”不等回车就出 1 条结果、`<mark>` 高亮、121 KB · 2026-11-20；输“.p”出 3 条、按两次 ↓ 高亮封面.png、回车关闭；无匹配显示“没有找到”；底部是快捷键提示、无“关闭”按钮）。截图 02u/02v/02w/02x/02y 已目视：批量栏淡绿条在表格上方、勾选行淡蓝与选中行淡绿能分清、大小列右对齐带 ↑/↓、命令面板居中且结果行带大小日期、上传目标框正常。
- 构建/验证时修的真问题与 harness 调整：① 2e 原来点 `.file-table input[type="checkbox"]` 的第一个——表头复选框加上后第一个变成它，改成 `tbody` 限定（应用代码没错，是检查脚本要跟上）；② 2q 的 overlay“下一个文件”期望从“旅行.mp4”改成“合同.docx”（本版起 overlay 跟可见排序走，见 C）；③ 2e 的刷新按钮 title 期望改成“刷新全部容量”（本版改名）；④ harness 的 Search/PickFiles 桩是新加的（之前 Search 未实现会抛错）；⑤ 2v 的名称排序期望：中文名按拼音排在拉丁字母前（`localeCompare('zh')` 在桩 Chromium 里的行为：main.go 排在所有中文名之后）——不是 bug，是 locale 行为，断言按实测写。
- 诚实缺口：上传文件整条路（NSOpenPanel 真选框 → 上传）只能在用户 Mac 上验，harness 只验了接线；今天/昨天相对时间只在真机上出现（桩数据全是 6 天前）；名称排序里中文在前、拉丁字母在后是 Chromium 的 zh collation 行为，真机 Chrome 同款 ICU，应一致，待用户肉眼确认；Agent 角标在同步页增删任务后要回到浏览页才重数（不轮询）；v0.13.0/v0.14.0 的缺口（网格缩略图、Toast 悬停计时、PDF.js 真机、clipboard、视频全屏）原样保留。
