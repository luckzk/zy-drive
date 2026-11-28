# FUSE 挂载设计：把网盘挂成本地盘

> 状态：设计稿，2026-11-27。只设计、不写代码。实现前用户再拍板文末的待决策问题。
> 路线已定（用户 2026-11-27）：**FUSE 先行、File Provider 以后**。
> 2026-11-27 22:53：§19 八项已全部拍板，P0（v0.19.0）已实现、未推送；挂载点改为 ~/ZyDrive，覆盖正文所有 /Volumes/ZyDrive 写法；P0 只做 Drive、Dropbox、OneDrive。
> 现状基线：桌面端 v0.18.1 已推（636caa6c）；9 家驱动全部走官方 API 原生实现，
> 无 rclone；无任何 FUSE 代码；预览缓存（v0.9.1）与写回无关的只读副本机制已存在。

## 0. 一句话

桌面上起一个 FUSE 守护进程，把用户**开关选中的网盘**挂到一个虚拟盘
（`/Volumes/ZyDrive`）下、每盘一个顶层文件夹；文件平时只是占位符
（只有名字、大小、时间），打开时才从云端拉到本地缓存，保存时先落本地、
关文件后再传回云端。**挂载哪个盘，由用户在侧栏网盘列表里逐盘开关。**

## 1. 目标与非目标

**目标**

- 用户在 Finder 里像本地盘一样浏览、打开、保存 8 家云盘上的文件（本地文件夹除外）。
- txt 这类小文件长期在编辑器里开着、时常保存，体验接近本地文件：
  保存瞬间完成（写回缓存），断网也不报错。
- 跨电脑改动能被发现：远端变了，挂载这边在可接受的延迟内失效缓存，
  编辑器侧按编辑器自己的规则提示（有未保存修改时一定先问用户）。
- 挂载是显式动作：默认全部关，用户逐盘开；开关状态重启后恢复。

**非目标**

- 不做 File Provider（以后只换表面，见 §3）；不做 Windows（P2 再评估）。
- 不做 Agent 端挂载：Agent 是无界面执行端，没有 Finder，挂载只在桌面。
- 不把挂载当工作盘：大文件原地改、git、视频剪辑、数据库不在目标内（见 §15）。
- 不解决多设备同时改同一文件的合并问题：后写者胜，见 §10 与 §16。

## 2. 现状盘点（写设计前核实过的代码事实）

| 事实 | 来源 |
|---|---|
| Provider 核心 8 方法：List / Stat / OpenRead / Upload / Mkdir / Rename / Delete / Caps；其余能力全是可选接口（类型断言） | `internal/storage/provider.go` |
| DeltaSource（增量接口）只有 3 家实现：Drive、Dropbox、OneDrive；S3、Drime、Box、pCloud、MEGA、本地均无（2026-11-27 grep 核实） | 同上 + `internal/storage/providers/*/Delta` |
| ResumableUploader（可续传上传）只有 Drime 一家实现；其余 7 家写回只能整文件重传 | `internal/storage/providers/drime/resume.go`（grep 核实，全仓唯一） |
| Mount 结构：ID / DisplayName / Provider / RootRef / Group / SortOrder / Caps / Status / LoadError（内存态）/ ProxyRef / Verified；无 email 字段 | `internal/domain/mount.go` |
| 每盘的 ProxyRef 存在 mount 的 config map 里（键 `proxy_ref`），不新增 mounts 表列——开关状态可沿用同一先例 | `internal/app/accounts.go:472`、`internal/app/bind.go:315`（UpdateMountProxy） |
| 迁移最新到 v25（侧栏排序回填）；新表列要加迁移 v26，新 config map 键不用迁移 | `internal/cache/migrate.go` |
| 桌面进程：`main.go` 先起 `LoadMountsAsync()`（后台，每盘 30 秒超时）、`InitPreviewCache()`、transfer worker（`worker.Start(ctx)`），`signal.NotifyContext` 收 SIGINT/SIGTERM，窗口循环返回后跑 `ClearPreviewCacheOnExit()` | `cmd/desktop/main.go` |
| 预览缓存：macOS `~/Library/Caches/com.luckzk.zy-drive/preview`；键 `sha256(mount_id + "\x00" + ref)`；清单在 `preview_cache` 表（大小、远端 mtime、最后访问）；临时文件 + 改名原子写、写完置 0444 只读；LRU 清理 + 5 分钟内访问过的不清；默认 2 GB，设置档 500 MB / 1 GB / 2 GB / 5 GB | `internal/app/preview_cache.go` |
| 设置是通用 KV（`GetSetting` / `SetSetting`，`settings` 表） | `internal/cache/settings.go` |
| 前端只允许 `frontend/src/api/client.ts` 调后端；新功能要加 Bind（`internal/app/bind.go` 的 `DesktopService`） | `internal/app/bind.go` |
| `go.mod`：Go 1.27.3，无任何 FUSE 依赖；mygo v0.10.0 | `go.mod` |
| `docs/provider-capabilities.md` 只覆盖 5 家（OneDrive/Drive/S3/Drime/Dropbox）；Box/pCloud/MEGA 的字段与操作以代码 grep 为准，本设计只写已核实的 | 同上 |
| 挂载点现状：无任何 FUSE 代码，无挂载点、无卷管理、无缓存写回 | 全仓 grep |

**由上表直接得出的四个设计约束：**

1. **写回只能整文件重传（Drime 除外）**——Provider 只有 `Upload(ref, r, size)`，
   没有 Range 写；只有 Drime 实现了 `UploadResumable`。所以 txt 这类小文件很合适，
   大文件原地改不合适（§15），这是接口现状决定的，不是实现能绕过去的。
2. **远端变化发现只能分盘做**——只有 Drive/Dropbox/OneDrive 有 DeltaSource，
   S3/Drime 只能轮询全量比对，Box/pCloud/MEGA 实现时再核查（§11）。
3. **开关状态不用新迁移**——沿用 `proxy_ref` 的先例，放 mount 的 config map，
   键 `mount_enabled`（"1"/"0"，缺省 "0"=关），见 §6。
4. **守护进程形态跟着现有模式走**——`main.go` 里已经有后台 goroutine
   （LoadMountsAsync、transfer worker），挂载守护进程同款，不另起二进制（§5）。

## 3. 已定方向（2026-11-27 用户定，不可推翻）

- **路线：FUSE 先行、File Provider 以后。** File Provider 是大厂正路，但对 Go
  团队工作量大几倍（Swift 扩展 + IPC + 签名打包；Go 只有调用侧绑定
  `github.com/tmc/apple/fileprovider`，没有实现扩展的库）。FUSE 在 2026 年
  已不用内核扩展（FUSE-T 本地 NFS 桥接，macOS 26 有官方 FSKit）。
- **按需核心与呈现表面无关。** 占位符元数据、按需拉取、本地缓存与清理、
  固定保留、写回上传——这套核心先做 FUSE 不浪费；File Provider 以后只换表面
  （同一套核心 + 同一个缓存 + 同一套 Provider 调用）。
- **形态：一个虚拟盘，每盘一个顶层文件夹。** 例如 `/Volumes/ZyDrive` 下
  `Dropbox/`、`OneDrive/`、`Drime/`……各一个。
- **8 家云盘都能挂，本地文件夹除外**（它本身就是磁盘，挂它没意义）。
- **写回缓存，不要写直通。** 保存先落本地、关文件后再传，保存体感瞬间完成；
  断网时保存也不报错（§10）。
- **挂载哪个盘由用户在侧栏网盘列表里自己选，每盘给一个开关**（§6）。

## 4. FUSE 库选型

候选只剩两个能上 macOS 的 Go 库（其余如 `jacobsa/fuse` 已多年无人维护，
`winfsp/cgofuse` 是 Windows 优先的 cgo 方案，不在本次比较内——Windows 是 P2）。

| 维度 | hanwen/go-fuse/v2（推荐） | bazil.org/fuse（上游） |
|---|---|---|
| 维护状态 | 活跃维护；协议支持到 FUSE 7.12.28 | 上游已停滞；macOS 支持 2020 年被作者主动删掉（commit 60eaf8f，理由是 OSXFUSE 闭源 + 苹果在杀这个品类） |
| macOS 支持 | 支持 macFUSE 与 FUSE-T 两种后端；`mount_darwin.go` 不绑定某一家，二进制不挑驱动（gmountie 文档核实，2026 年） | 上游不支持 macOS；rclone 的 bazil 后端是 Linux-only；想上 macOS 只能用 zegl/anacrolix 这类 fork（恢复了 MacFUSE 4 + FUSE-T + M1 支持），等于把依赖押在一个 fork 上 |
| 谁在用 | rclone 的 `mount2`（Linux + macOS）就是它；gocryptfs、JuiceFS 等同类挂载工具的主流选择 | rclone 的 `mount`（仅 Linux） |
| API 形态 | inode 式 API（`fs.Inode` + 可选接口），高层 `fs` 包够用；文档与示例全 | node 式 API；文档少 |
| 构建 | 纯 Go，无 cgo——与现有工具链（Go 1.27.3，无 cgo 依赖）零冲突 | 纯 Go，但与上条无关 |
| macOS 已知短板 | 作者本人没有 Mac（接受补丁）；**不支持 NOTIFY**（内核收不到变化通知）；macOS 会持续发 STATFS；并发读有性能问题 | 同样受 macOS 侧限制，且无人修 |

**推荐：`github.com/hanwen/go-fuse/v2`（v2 最新版）。**

核实依据（不是凭记忆）：

- gmountie 文档（2026 年）："Mounting on macOS needs a FUSE provider — either
  macFUSE (kext) or FUSE-T (userspace, no kext). `hanwen/go-fuse`'s
  `mount_darwin.go` works with either; the binary doesn't tie itself to one."
  （https://docs.gmountie.dev/design/operations-and-packaging/）
- rclone 的 FUSE 后端对照：`mount`（bazil，Linux-only）、`cmount`（cgofuse，
  跨平台）、`mount2`（go-fuse/v2，Linux + macOS）。
- bazil 删 macOS 支持的 commit 说明（2020）与 zegl/anacrolix fork 的
  README（socket.dev 包页）。

**go-fuse 短板对本设计的直接影响（已计入，不回避）：**

- 不支持 NOTIFY → 远端变化**不能**靠内核通知推给 Finder/编辑器，
  只能由守护进程自己发现（delta/轮询）再失效缓存（§11）。
- STATFS 频繁 → `Statfs` 回调必须便宜（返回缓存的固定值，不调任何云 API）。
- 作者无 Mac → **macOS 侧只能在用户 Mac 上验**（§18 有清单）。

**未核实、实现时核实：** go-fuse 在 darwin/arm64 上的构建与挂载。
它是纯 Go，按理能编，但 rclone 的对照表里 `mount2` 的 macOS 栏只写了 amd64
（那是 rclone 自身的构建约束，不一定是 go-fuse 的）。P0 验证第一项就是
在用户 Apple Silicon Mac 上编出来并挂上。

## 5. 总体架构

```
Finder ── /Volumes/ZyDrive ── FUSE 内核侧（FUSE-T/macFUSE）
                                   │  go-fuse/v2 Server（桌面进程内 goroutine）
                                   ▼
                          internal/mount（新包）
                            ├─ MountService：卷生命周期 + 每盘开关 + 状态机
                            ├─ fuseFS：根 → 每盘一个 Inode → Provider 节点
                            ├─ mountCache：本地缓存（§8，独立于预览缓存）
                            ├─ uploader：写回上传 worker（走 transfer limiter）
                            └─ changeWatcher：每盘一个 goroutine，delta/轮询（§11）
                                   │  只调 storage.Provider 8 方法 + 可选接口
                                   ▼
              各家驱动（Dropbox/Drive/OneDrive/S3/Drime/Box/pCloud/MEGA）
```

**守护进程形态：桌面进程内跑 goroutine（决定）。**

- 跟 `main.go` 现有模式一致：`LoadMountsAsync()`、`transfer worker`
  都是桌面进程内的后台 goroutine；挂载守护进程同款，在 `runDesktop` 之前起，
  退出清理时卸卷（见下）。
- 不另起独立进程/二进制的理由：mygo 桌面是单进程模型；独立进程要多一套
  IPC（Unix socket + 协议）与进程监管，P0 不付这个成本；崩溃恢复也不因此
  变简单（见下）。
- **桌面退出时：** 先尝试把 dirty 缓存刷完（限时 30 秒，见 §10），再卸卷，
  最后跑现有的 `ClearPreviewCacheOnExit()`。退出时有文件正打开 → 弹确认
  （"仍有 N 个文件打开，退出后挂载断开；未上传的改动保留在本地缓存，
  下次启动续传"），用户取消则不退出。
- **崩溃/被杀时：** 守护进程随桌面进程一起死，卷变 stale（Finder 里显示
  未连接）。下次启动时先检测挂载点是否 stale（能 stat 挂载点目录但
  FUSE 服务端已死），是则先强制卸卷再重挂。**不承诺**崩溃瞬间正在写的
  数据不丢：已 close 且已落缓存的在，正在写（未 close）的那次写可能丢一半，
  与本地磁盘断电的语义相同，文档与提示里如实写。
- **Agent 不挂载。** Agent 是无界面执行端，没有 Finder；挂载只在桌面。
  同步任务跑在 Agent 上，与挂载无关。

**新增代码位置（建议，不写代码只定位置）：**

- `internal/mount/`：MountService、fuseFS、mountCache、uploader、changeWatcher。
- `internal/app/bind.go`：新 Bind——`GetMountStatus`、`SetMountEnabled(mountID, enabled)`、
  `GetMountCacheInfo`、`ClearMountCache`、`RetryMount(mountID)`。
- `internal/cache/migrate.go`：新迁移 v26——`mount_cache` 表（§8）。
- 前端：`Sidebar.tsx` 每盘开关 + 状态点；`Settings.tsx` 新增"挂载"卡片（§13）。

## 6. 每盘开关（用户 2026-11-27 22:37 定）

挂载哪个盘，由用户在**侧栏网盘列表**里自己选，每盘一个开关。

### 6.1 交互

- **位置：** 侧栏每张挂载卡片上，常显一个小开关（复用设置页 Switch 的
  样式，44×24 缩小版；开 = 品牌绿）。开关是挂载功能的主操作，**不**沿用
  删除按钮的"悬停才出现"规则——藏起来的开关用户找不到。
- **本地文件夹挂载不给开关。** 它本身就是磁盘，挂它没意义；卡片上不画
  开关（不画禁用态，禁用态像个坏掉的功能）。
- **默认：全部关。** 挂载是显式动作，不替用户开；用户逐盘打开。
- **开关联动：** 开关打开 → 该盘文件夹出现在 `/Volumes/ZyDrive` 下；
  关闭 → 该盘文件夹从挂载点消失。**不重挂整个卷**（见 6.3）。

### 6.2 开关状态存在哪

- **存在 mount 的 config map 里，键 `mount_enabled`，值 "1"/"0"，缺省 "0"（关）。**
  沿用 `proxy_ref` 的先例（`internal/app/accounts.go:472`）：不新增 mounts 表列，
  不新增迁移；`ListMounts` 返回的 `domain.Mount` 里加一个内存字段
  `MountEnabled bool`（`json:"mountEnabled"`），由 config map 填充，
  与 `ProxyRef` 同款做法。
- 前端调新 Bind `SetMountEnabled(mountID, enabled)` 改开关；改完立即写库，
  不需要另点保存。

### 6.3 开/关时挂载点发生什么（不重挂卷）

- **开：** 该盘加入 MountService 的 enabled 集。根目录的 readdir 从此列出
  该盘文件夹；之前该盘不存在于挂载点，打开即按 §9 按需拉取。
  若卷当前未挂载（例如全部盘都关着），先挂载卷，再列出该盘。
  若该盘尚未加载成功（LoadError / auth_expired），开关拒绝打开并在卡片上
  显示原因（"该网盘加载失败：…"），不静默失败。
- **关：** 该盘从 enabled 集移除。根目录的 readdir 从此不再列出它；
  之后再打开该盘路径返回 ENOENT（文件不存在）。**已经打开的文件句柄
  不受影响**：读、写、关闭、写回上传照常走完——守护进程跟踪每个盘的
  open 计数，关开关时若计数 > 0，在卡片上提示"仍有 N 个文件打开，
  关闭后不再能打开，正在编辑的文件不受影响"。
- **卷的生死跟着开关走：** 至少一盘开 → 卷存在；全部盘关 → 卷卸载
  （`Server.Unmount()`）。

### 6.4 桌面重启后是否恢复

- **恢复。** 启动时读各盘的 `mount_enabled`，自动挂载卷并恢复上次开启的盘。
  恢复失败（例如 FUSE 驱动被卸了、某盘凭证过期）时：开关**保持开位**
  （不替用户把开关拨回去），但该盘卡片标红 + 人话原因，卷照常挂载其余盘。
- 恢复发生在 `LoadMountsAsync` 完成之后：开关恢复要等 provider 实例建好，
  否则开出来的是空壳。启动顺序：窗口 → mounts 加载 → 恢复开关 → 挂载卷。

### 6.5 某个盘挂载失败时开关与列表如何反馈

- **失败只影响该盘。** 其余盘照常出现在挂载点；卷不因此整体失败。
- 开关保持开位，卡片状态点变红，原因写人话、不贴原始堆栈，例如：
  "需要重新授权"（auth_expired）、"连不上网盘，检查网络或代理"、
  "没有装 FUSE-T 或 macFUSE"（驱动缺失）、其余未知错误贴原始错误的一行。
- **重试 = 把开关关掉再打开。** 不另设重试按钮；`SetMountEnabled`
  关→开即重新走一遍加载 + 列出。设置页挂载卡片里另有一个"全部重试"入口
  （P1，未进 P0）。

## 7. 挂载点管理

- **挂载点：`/Volumes/ZyDrive`，卷名 `ZyDrive`。** macOS 惯例，Finder 侧栏
  "位置"下出现，与 OneDrive/Dropbox 的卷并列。
  **未核实：** 普通用户能否在 `/Volumes` 下建挂载点（FUSE 侧通常由
  mount helper 处理，不用 sudo，但须在用户 Mac 上实测）；备选 `~/ZyDrive`，
  实现时若权限有问题再换，挂载点做成常量、不散落各处。
- **顶层文件夹名 = Mount.DisplayName 清洗后：** 非法字符（`/`、`:`、首尾空格
  等）变 `_`；重名（两个盘同名）加 `-2`、`-3` 后缀；文件夹名与挂载名的映射
  记在 MountService 内存里，不写库（DisplayName 改了就跟着变，下次挂载重算）。
- **挂载流程：** 至少一盘开 → `Server.Mount()` → 卷出现。挂载失败
  （驱动缺失、挂载点被占）→ 设置页挂载卡片 + 首次开开关时弹引导（§12），
  不静默。
- **卸载流程：** 全部盘关，或桌面退出 → `Server.Unmount()`。
  卸载失败（有文件正打开）→ 拒绝卸载并提示"有 N 个文件正打开，
  关闭后重试"；**不**强制卸载正在使用的卷（强制卸会丢正在写的数据）。
  stale 卷（崩溃残留）下次启动先强制卸再重挂（§5）。
- **盘符名：** macOS 没有盘符概念，卷名即 `ZyDrive`；以后 Windows 再议。

## 8. 缓存设计

### 8.1 放哪、多大

- **位置：** `~/Library/Caches/com.luckzk.zy-drive/mount`——与预览缓存
  `…/preview` 平行、同级。macOS 惯例是 UserCacheDir 下按 bundle 分目录，
  与 `previewCacheDir()` 同款做法。
- **上限：默认 5 GB，建议档位 2 / 5 / 10 / 20 GB（待拍板，§19 Q1）。**
  预览缓存默认 2 GB 是只读副本够用；挂载缓存是工作集（用户正在编辑的文件
  全在里面），5 GB 是建议起点。
- **清单：** 新表 `mount_cache`（迁移 v26）：key、mount_id、ref、filename、
  size、remote_mtime、local_mtime、dirty（0/1）、last_access、pinned（0/1）。
  键沿用预览缓存的 `sha256(mount_id + "\x00" + ref)`；文件名保留原名
  （清洗后，key + 原名 ≤200 字符），与预览缓存同款做法。
- **与预览缓存的关系：目录与清单完全独立，只复用模式。**
  预览缓存是只读一次性副本（写完置 0444）；挂载缓存是可写工作集，带 dirty 态。
  生命周期不同，**不共用目录、不共用表**；只复用键算法、文件名清洗、
  临时文件 + 改名原子写、LRU + 5 分钟保护这几个模式（小函数级复用可以，
  不强行合并不相干的 manager）。

### 8.2 清理策略

- **LRU：** 超上限时按最后访问时间清最旧的；5 分钟内访问过的不清
  （与预览缓存同款保护窗口）。
- **dirty 的不清：** 未上传完的改动是用户数据，超上限也不清；超上限时
  新下载被拒并提示"缓存满了，先清理或调大上限"（§9）。
- **pinned 的不清：** 见 8.3。
- **启动时对账：** 清单有行、磁盘无文件 → 删行；磁盘有文件、清单无行
  （崩溃残留的 `.part` 临时文件等）→ 删文件；与预览缓存 `reconcileLocked`
  同款做法。
- **"清理缓存"按钮：** 只清未 dirty、未 pinned 的；dirty 的留着（那是
  未上传的改动），清完报 freed 字节数；与预览缓存"清理只清本缓存"同款规则。

### 8.3 固定保留（pinning）

- **P0：隐式保留。** 正在打开的文件 + 5 分钟内访问过的文件不参与清理。
  没有显式 pinning UI——P0 先把缓存跑通。
- **P1：显式固定保留。** 文件右键（ZyDrive 界面内）"固定保留" → `pinned=1`，
  永不参与 LRU；设置页挂载卡片列出已固定文件，可逐个取消。
  Pinning 只影响清理，不影响上传（固定文件照常写回）。

## 9. 读路径

- **打开文件：** Stat（先查目录缓存，未命中调 Provider.Stat）→
  查挂载缓存：size + 远端 mtime 一致且本地文件存在 → 直接读本地；
  未命中 → `OpenRead` **全量下载**到缓存（临时文件 + 改名），再读。
- **P0 全量下载，不做 Range/流式读。** Provider 只有 `OpenRead` 流式接口，
  没有 Range 读接口；要流式读需给 Provider 加可选 `RangeRead` 接口
  （OneDrive/S3/Dropbox/Drive 有 Range 能力，Drime 代理流需核实），那是 P1。
  **诚实写明体验：** 大文件首次打开要等整文件下完，期间编辑器显示加载中；
  打开过一次之后就是本地速度。单文件上限以各家 Caps 为准
  （Drive 5TB、S3 5TB、OneDrive 250GB、Dropbox 2TB），但实际受缓存上限
  与磁盘剩余空间限制。
- **缓存放不下时：** 文件大于缓存剩余空间 → 先触发 LRU 清理；仍放不下 →
  open 返回 ENOSPC，编辑器侧看到"磁盘空间不足"类提示，
  ZyDrive 界面同时给人话提示"文件大于挂载缓存可用空间，请调大上限或先下载"。
  **不**悄悄改走流式直读（P0 没有这条路）。
- **读并发与限速：** 复用现有 transfer limiter（与传输共用一个限速器），
  P0 不另设；读太多把同步/传输挤慢是已知取舍，P1 再分。
- **只读文件（Drive 原生文档等）：** 见 §14。

## 10. 写路径

**已定：写回缓存（write-back），不要写直通。**

- **写：** 所有写操作先落挂载缓存里的本地文件；编辑器每次保存都只是
  写本地，体感瞬间完成。**断网时保存也不报错**——本地已落盘就是成功。
- **上传触发：** 文件 close（或 flush）时记 dirty，上传 worker 按 close
  顺序异步上传（走 transfer limiter）。应用退出时有 dirty → 限时 30 秒
  尽量刷完，刷不完的留在缓存、下次启动续传（§5）。
- **上传前比对：** 先 Stat 远端，比 size + mtime（能拿到可信 hash 时比 hash）。
  没变 → `Upload` 整文件（S3/Drime 整对象重传；只有 Drime 有
  `UploadResumable` 可续传，其余全量重传——§2 已核实）。
  变了（别的电脑改过）→ **不覆盖**：本地 dirty 文件改名
  `name (conflict copy from this Mac <yyyy-MM-dd>).ext` 上传，界面提示用户
  "另一台电脑也改了这个文件，你的版本已另存为冲突副本"。
  （冲突命名的 `name (conflict copy from <side> <date>)` 格式沿用同步引擎
  conflict.go 的既有约定，`internal/sync/conflict.go:42`。）
- **上传失败：** 保留 dirty，定时重试 3 次（退避 1 / 5 / 15 分钟）；
  仍失败 → 该盘卡片标"有 N 个文件上传失败" + 文件名清单，用户可手动重试。
  **失败不丢本地改动**：dirty 文件一直在缓存里，直到上传成功或用户手动删。
- **新建文件：** create → 缓存建空文件 → close → Upload（远端无该文件则直接传）。
- **删除/重命名：** 直接调 Provider Delete/Rename（不经过缓存）；
  本地缓存副本同步清除。S3 文件夹删除残留的已知缺口同样适用（§14）。
- **编辑器原子保存：** 多数编辑器是"写临时文件 + 改名覆盖"。
  FUSE 层面看到 create tmp + write + rename：rename 时缓存文件跟着改名
  并记 dirty；中途的 tmp 文件 close 前就被 rename 掉，不单独进上传队列。
- **0 字节文件：** Upload 0 字节，各家都支持（S3 零字节对象 OK），不特殊处理。

## 11. 目录列表缓存与远端变化发现

### 11.1 FUSE 侧缓存

- **readdir：** 调 Provider.List，结果缓存，TTL **建议 30 秒（待拍板，§19 Q3）**；
  TTL 内重复打开同一文件夹不调云。go-fuse 的 EntryTimeout/AttrTimeout
  同样设短（秒级），不要让内核长期缓存旧属性。
- **Stat：** 优先用目录缓存里的条目；未命中调 Provider.Stat。
- **失效：** 本盘通过写路径改了东西（新建/删除/改名/上传成功）→ 立即失效
  对应目录的缓存；远端变化 → 由 11.2 的发现机制失效。

### 11.2 远端变化发现（分盘说明）

go-fuse 在 macOS 上**不支持 NOTIFY**（§4），内核不会主动收到变化通知；
发现必须由守护进程的 changeWatcher 主动做。

| 盘 | 机制 | 说明 |
|---|---|---|
| OneDrive | `/delta`（DeltaSource 已实现） | 轮询 delta 游标，有变化才拉清单 |
| Drive | `changes.list`（已实现） | 同上 |
| Dropbox | `longpoll`（已实现） | 长轮询，有变化再拉 |
| S3 / R2 | 定时全量 List 比对 | 无 delta；只轮询**已访问过**的目录；间隔建议 5 分钟（待拍板，§19 Q2） |
| Drime | 定时全量比对 | 无 delta；429 惩罚约 15 小时，轮询必须克制：只轮询已访问过的目录，间隔建议 5 分钟；轮询失败一次就停、报一次，不循环重试 |
| Box / pCloud / MEGA | **未核实** | 代码里三家都没有 Delta 实现；实现时核查各家有无增量接口，有则接 DeltaSource，无则走轮询（同 S3/Drime 的克制规则） |
| 本地 | 不挂载 | — |

- **发现变化后的动作：** 失效对应目录的列表缓存；受影响文件的缓存项
  （size/mtime 变了）失效，下次读重下；正在打开的文件**不**强制换内容
  （换内容会打断正在编辑的用户），只让编辑器自己发现。
- **编辑器侧：** 靠编辑器自己的文件监视（mtime/size 变化）弹提示。
  没未保存修改 → 多数编辑器自动重载；有未保存修改 → 一定先问用户。
  **后写者胜的风险如实写进界面文案**：若用户没注意提示直接保存，
  内存里的旧内容会覆盖另一台电脑的改动——这是所有挂载与同步盘共有的语义，
  不是 ZyDrive 特有的，文档与提示里不藏着。
- **轮询只针对已访问过的目录**，不是全盘扫描：没打开过的目录不需要新鲜度。

## 12. FUSE-T/macFUSE 检测与引导安装

- **预检：** 挂载卷之前检查驱动在不在：macFUSE（`/Library/Filesystems/macfuse.fs`
  存在）或 FUSE-T（fuse-t 安装存在）。**确切检测路径实现时在用户 Mac 上核实**，
  本设计不编造路径——只写检查方法（存在性检查 + 挂载试错分类）。
- **缺失时：** 设置页挂载卡片显示"未安装 FUSE 驱动"，首次开开关时弹引导：
  推荐 FUSE-T（`brew install --cask fuse-t` 或官网 pkg，无内核扩展），
  备选 macFUSE（官网下载，需进恢复模式批内核扩展——Apple Silicon 上很劝退，
  只作备选）。**ZyDrive 不替用户装驱动**（要管理员密码 + 系统级安装，
  必须用户自己点）。
- **挂载失败分类：** 沿用 gmountie 的做法——挂载错误按模式分类：
  含 `macfuse` / `osxfuse` / `mount_macfuse` / `/dev/macfuse` 等字样 →
  "没有装 FUSE 驱动"并给安装指引；其余错误贴原始错误一行。
- **macOS 26 说明：** macOS 26 有官方 FSKit，FUSE-T 1.2.1+ 与 macFUSE 5.1+
  都能走 FSKit（不用内核扩展也不用 NFS 绕路）；用户机器若已是 macOS 26，
  引导文案优先写 FSKit 路径。用户的 macOS 版本实现时再确认（不在本设计里假设）。

## 13. 设置与界面

- **侧栏（主界面）：** 每盘卡片常显小开关（§6）+ 状态点：
  绿 = 已挂载，灰 = 未挂载（开关关），转圈 = 挂载中，红 = 失败（悬停/点击看原因）。
  开关旁边不另加"挂载"文字按钮——开关本身就是操作。
- **设置页新增"挂载"卡片：** 卷状态（已挂载/未挂载/失败原因）、挂载点
  （`/Volumes/ZyDrive`）、FUSE 驱动状态（FUSE-T / macFUSE / 未安装 +
  安装指引入口）、缓存上限（档位，§8）、缓存大小/文件数 + "清理缓存"、
  已固定文件列表（P1）、退出时行为说明。
- **新 Bind（§5 已列）：** `GetMountStatus`（卷状态 + 每盘状态 + 驱动状态）、
  `SetMountEnabled`、`GetMountCacheInfo`、`ClearMountCache`、`RetryMount`。
  挂载状态变化走现有轮询模式（前端每 2 秒轮询 `MountsLoading` 的先例在
  v0.18.0；挂载状态同款轮询，不另发明推送机制）。
- **不做的界面：** 不在 Finder 里加右键菜单（那是 File Provider 扩展的活）；
  不在挂载卷里放说明文件（README 进挂载根目录是别家的做法，P0 不做）。

## 14. 各家差异约束表

| 盘 | 挂载时的约束 |
|---|---|
| Drive | Google 原生文档（Docs/Sheets/Slides）没有可下载字节，驱动经 files.export 导出成 Office 格式打开（docx/xlsx/pptx，绘图导出 PNG；2026-11-28 用户定：接受导出打开）。普通上传的文件正常。 |
| S3 / R2 | 无真实文件夹（零字节占位对象 + `/`）；改名是 CopyObject + DeleteObject（非原子，大文件改名慢）；写回整对象重传；文件夹删除只删占位对象、内容残留（capabilities §三.5 的已知缺口，挂载删除同样中招）；R2 无版本控制；无 quota（容量行照常显示"不限量"）。 |
| Drime | multipart ≥5 MB；写回整文件重传，但有 `UploadResumable` 可续传（唯一一家）；无 delta（轮询，§11）；429 惩罚约 15 小时，轮询与重试必须克制；`hash` 字段是 URL 安全 ID、不是内容哈希，上传前比对只能用 size + mtime。 |
| OneDrive | 路径总长约 400 字符：超长路径的深文件夹打不开（open 报错 + 人话提示）；上传会话分片 5 MB；delta 有。 |
| Dropbox | 基础读写正常；`move_v2` 改名；longpoll 有；content_hash 是 Dropbox 内部算法，不跨云比。 |
| Box / pCloud / MEGA | 基础读写正常；Delta 未核实（§11）；MEGA 会话过期 → 该盘标 auth_expired、提示重新授权（复用 v0.9.3 机制）；Box/pCloud 换 token 要 Client Secret（自建应用），与挂载无关但失败时原因要写对。 |
| 本地 | 不挂载，无开关。 |

## 15. 不适合的场景（写明，不藏着）

- **大文件原地改**（视频剪辑、虚拟机镜像、数据库文件）：每次保存都整文件
  重传，改一个字节也要传整个文件；且首次打开要等整文件下完。
- **git 仓库放挂载盘上：** git 大量小文件随机读写 + 频繁 stat，FUSE 往返
  + 云端延迟会把速度拖垮；且 `.git` 目录的锁文件语义在部分云上不可靠。
- **数据库/频繁小写的程序文件：** SQLite、IDE 索引库等每次写都触发整文件
  上传，写放大严重。
- **超大目录浏览：** Drime/S3 无 delta，大文件夹每次列目录都是全量扫；
  上万文件的目录打开会慢（列表缓存 TTL 内可缓解，首次仍慢）。
- 以上场景的正确姿势仍是 ZyDrive 界面里的同步任务（整盘同步到本地），
  不是挂载。

## 16. 安全与风险

| 风险 | 说明与应对 |
|---|---|
| 缓存文件是云端数据的明文副本 | 与预览缓存同级风险：落在用户本机缓存目录，依赖 FileVault 磁盘加密；**不**另做缓存加密（P3 客户端加密做的是云端密文，与本地缓存无关）。卸载后缓存保留（不清），除非用户点清理。 |
| dirty 未上传数据的丢失窗口 | 本机磁盘坏 / 缓存被用户手动删 → 未上传的改动丢。界面文案如实写"未上传的改动只在本地"；退出时限时刷完（§5）；不承诺断电不丢。 |
| 多设备同时改同一文件 | 后写者胜（§10 冲突改名是事后补救，不是合并）。界面与文档都写明，不藏着。 |
| FUSE 驱动是第三方 | FUSE-T/macFUSE 不是我们写的；驱动有 bug 会影响挂载稳定性。应对：驱动缺失/异常时明确提示，不把驱动错误包装成我们的错误；驱动版本不由我们控制。 |
| 误删风险 | 挂载盘上的删除直接调 Provider Delete（Dropbox/Drive/OneDrive/Drime 进回收站，S3 永久删除）；Finder 里删文件没有 ZyDrive 界面的二次确认——P0 在文档里写明，P1 评估挂载内删除是否加确认（Finder 侧无法拦截，只能事后进回收站恢复）。 |
| 速率与 429 | Drime 轮询 + 上传失败重试都必须克制（§11、§10）；S3 全量 List 比对只针对已访问目录。 |

## 17. 实施顺序

### P0（先跑通：挂载 + 开关 + 按需读 + 写回）

1. go-fuse/v2 接入 + `internal/mount` 新包 + 卷挂载/卸载（`/Volumes/ZyDrive`）。
2. 每盘开关（config map `mount_enabled` + `SetMountEnabled` Bind + 侧栏开关）。
3. 按需读：Stat + 缓存命中 + OpenRead 全量下载；目录列表缓存 30 秒。
4. 写回缓存：写落本地、close 记 dirty、上传 worker 异步传、失败保留 dirty。
5. 缓存：`mount_cache` 表（v26）+ LRU + 5 分钟保护 + 清理按钮。
6. 驱动检测与引导安装（FUSE-T 优先）。
7. 真机验证：Drime + R2/S3 先行（§18 清单）。

### P1（体验补齐）

1. Range/流式读（Provider 加可选 `RangeRead` 接口；大文件打开不用等整文件）。
2. 远端变化发现：delta/longpoll 接入（Drive/Dropbox/OneDrive）+ S3/Drime 克制轮询。
3. 冲突处理完善（上传前比对 + 冲突改名 + 提示，P0 先做最小版：比对 + 不覆盖 + 提示）。
4. 显式固定保留（pinning UI + 设置页列表）。
5. 缓存上限档位、退出时 dirty 处理细化。

### P2（以后）

1. File Provider 切换：同一套按需核心 + 同一个缓存，只换表面（Swift 扩展壳）。
2. Windows：cgofuse/WinFsp 评估（与 macOS 不同的库选型，不在本次设计内）。
3. 挂载内删除确认、全部重试入口等小项。

## 18. 诚实缺口与 Mac 真机验证方案

**以下全部只能在用户 Mac 上验，VM 里验不了（本 VM 无 macOS、无 FUSE 驱动）：**

| 验证项 | 怎么验 |
|---|---|
| darwin/arm64 构建 + 挂载 | `make build` 出桌面二进制，在用户 Apple Silicon Mac 上挂载成功 |
| FUSE-T 安装与检测 | 用户装 FUSE-T（brew cask 或 pkg），ZyDrive 检测到并挂载；再卸掉看引导提示是否出现 |
| `/Volumes/ZyDrive` 挂载点权限 | 普通用户挂载，不用 sudo；若不行换 `~/ZyDrive` |
| txt 编辑器保存全流程 | txt 长期在编辑器开着、多次保存：保存体感、断网保存、联网后续传 |
| 跨电脑改动 | 另一台电脑改同一文件，挂载这边多久发现、编辑器是否提示、保存是否覆盖 |
| 开关行为 | 逐盘开/关、重启恢复、单盘失败不影响其余盘、关盘时有文件正打开的提示 |
| 卸载/退出 | 全部关卸载卷；退出时 dirty 处理；崩溃后 stale 卷恢复 |
| 大文件首次打开 | 100 MB+ 文件首次打开的等待体感；缓存满时的提示 |
| Finder 侧栏 | 卷出现在 Finder"位置"下；顶层文件夹名与重名后缀 |
| 退出清理 | "退出时清理缓存"开时，挂载缓存是否被误删（不应该：挂载缓存与预览缓存独立） |

**VM 里能验的（P0 完成时做）：** go-fuse 在 Linux 下的编译与基本挂载
（Linux 有内核 FUSE，可跑通读/写/开关逻辑）；Provider 侧用 fake/mock
（已有 contracttest 模式）；缓存 LRU 与 dirty 状态机单测。
**VM 里验不了、也不假装验了的：** macOS 上的一切（见上表）。

## 19. 待拍板问题（2026-11-27 22:53 用户已全部拍板，P0 v0.19.0 已按此实现、未推送）

1. **缓存上限：** ~~默认 5 GB、档位 2 / 5 / 10 / 20 GB 是否接受？还是沿用预览缓存的 2 GB 默认 + 500 MB / 1 / 2 / 5 GB 档？~~ → **已定：默认 5 GB，档位 2 / 5 / 10 / 20 GB。**
2. **轮询间隔：** ~~S3/Drime 远端变化轮询建议 5 分钟（仅已访问过的目录）是否接受？Drime 的 429 惩罚下要不要更长？~~ → **已定：5 分钟（仅已访问过的目录）；P0 不做 S3/Drime，轮询逻辑已写好但不启用。**
3. **目录列表缓存 TTL：** ~~建议 30 秒是否接受？（越短越新鲜、越多云 API 调用。）~~ → **已定：30 秒，按目录懒刷新——访问哪个目录才刷哪个目录的列表，绝不是全量刷新所有目录。**
4. **退出时有未上传 dirty：** ~~限时 30 秒尽量刷完再退，还是直接退、下次启动续传？（前者退出慢，后者下次启动才传。）~~ → **已定：退出时弹提示对话框，由用户选“不退出继续等待”还是“直接退出、下次续传”。**
5. **免费还是 Pro？** ~~roadmap 第 16 项原来就写"分界线待定"——挂载免费还是 Pro，**不自作主张**，等用户定。（参考：Air Explorer 挂载类功能在 Pro；Mountain Duck 整个产品收费；rclone 免费。）~~ → **已定：免费。**
6. **P0 先做哪几家盘？** ~~建议读/写全上 8 家（接口统一），真机验证先 Drime + R2/S3；还是 P0 只上两三家、其余 P1 再加？~~ → **已定：P0 先做 Drive、Dropbox、OneDrive；其余 5 家 P0 不显示挂载开关（Caps 规则：能力缺失隐藏，不置灰）；本地盘无开关。**
7. **大文件打不开时：** ~~文件大于缓存可用空间，拒绝打开 + 提示（P0 方案）是否接受？还是 P1 的流式读提前做？~~ → **已定：接受，拒绝打开 + 提示。**
8. **挂载点：** ~~`/Volumes/ZyDrive` 还是 `~/ZyDrive`？（实现时核实 `/Volumes` 权限后定，用户可先选倾向。）~~ → **已定：`~/ZyDrive`（用户主目录下）。**
