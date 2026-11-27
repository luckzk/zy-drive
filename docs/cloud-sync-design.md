# 云同步设计：用自己的云盘同步配置

> 状态：设计稿，2026-11-27。用户已定方向：**用自己的云盘同步**（不用我们搭同步云，不走 Agent 中转）。
> 只设计、不写代码。实现前用户再拍板文末的待决策问题。
> 现状基线：配置只在单机（SQLite + master.key），跨设备靠手动导出/导入加密配置包
> （`internal/agentapi/bundle.go`，Bundle v1，PBKDF2 100k + AES-256-GCM，口令 ≥8 位）。

## 0. 一句话

桌面改了配置 → 把**加密后的记录**写到用户自己选的一朵云（载体）上的固定目录 →
手机（和另一台桌面）打开时来拉。**云盘厂商只能看到不透明的文件，看不到任何凭证；**
**我们没有服务器参与**，沿用"凭证不过我们的服务器"那条产品原则。

## 1. 目标与非目标

**目标**

- 网盘（Mount + 凭证）、同步任务、部分设置，在用户自己的多台设备间自动保持一致。
- 首次接入仍走一次手动导入（见 §5 鸡生蛋问题），之后不再手动。
- 云盘厂商、中间人即使拿到载体上的全部文件，也解不出任何凭证（端到端加密）。
- 任一设备丢了、坏了、重装了，只要还有一台设备在线就能恢复；载体上的文件被删光，
  本地数据也不丢（同步是合并，不是镜像）。

**非目标**

- 不做我们自己的同步云、账号系统、托管服务器（已由用户否决）。
- 不同步文件内容本身——文件本来就住在各家云里，两台设备直连同一朵云看到的就是同一份。
- 不解决手机后台同步（iOS 后台模式 / Android Doze 下定时同步受限，手机只能打开 App 时同步，
  见 `docs/mobile-app.md`）；定时同步任务仍由服务器上的 Agent 跑。
- 不同步运行记录（run history）：它住在 Agent 上，手机/桌面改为**直接连 Agent 查**（见 §3.5），
  不经过同步载体。

## 2. 现状盘点（写设计前核实过的代码事实）

| 事实 | 来源 |
|---|---|
| 配置包格式已存在：`Bundle{Version:1, ExportedAt, AgentName, Mounts, Jobs}`，`BundleMount{Mount, Config, Credentials}`；凭证字段含 AccessKey/SecretKey/AccessToken/RefreshToken/ClientSecret/MegaSession/MegaMasterKey | `internal/agentapi/bundle.go` |
| 加密参数：PBKDF2-SHA256 100k + AES-256-GCM，salt 16B，nonce 12B，口令 ≥8 位；解密失败只报错、不出半明文 | 同上（`EncryptBundle` / `DecryptBundle`） |
| 导入时凭证用**导入方自己的 master.key**重加密；重导按 ID 覆盖同名网盘与任务，运行记录保留 | `internal/app/bundle.go`（`ImportBundle`）、`docs/agent-deploy.md` |
| master.key 是**每台机器随机生成**的（salt 16B + password 32B），首次运行生成、永不覆盖 | `internal/auth/masterkey.go` |
| Mount 与 SyncJob 都有 `updated_at`（RFC3339，SQLite 与 domain 层都有） | `internal/cache/mounts.go`、`internal/cache/syncjobs.go`、`internal/domain/mount.go:102`、`internal/domain/syncjob.go:82` |
| 本地文件夹挂载/任务导不出去（对方没有这台电脑的磁盘），导出时明说跳过 | `docs/agent-deploy.md` §配置下发 |
| 设置是通用 KV（`GetSetting` / `SetSetting`） | `internal/cache/settings.go` |
| 前端只允许 `frontend/src/api/client.ts` 调后端；Bind 方法在 mygo 窗口外抛错 | `frontend/src/api/client.ts` |

**由上表直接得出的三个设计约束：**

1. **同步密钥不能从 master.key 派生**——每台设备的 master.key 都不同且随机。
   同步密钥必须来自一个**各设备共知的口令**（与配置包口令同机制、同参数），每台设备
   在首次接入时输入一次，之后存在本地 secrets store 里（用本机 master.key 加密存放），
   不用每次输入。
2. **按记录合并是可行的**——Mount 与 SyncJob 已有 `updated_at`，可以做逐条 last-write-wins。
   但有一个待核实的点：**token 刷新是否会推高 Mount 的 `updated_at`**。刷新只写 secrets 表的话，
   同步层看不到凭证变了。实现时必须核实刷新路径，必要时让刷新显式 bump 对应 Mount 的
   `updated_at`（或另加一个 `credentials_version` 字段）。这是 §7 的一部分。
3. **配置包格式可复用**——同步记录就是"把 Bundle 拆成逐条 + 清单"，不用发明第二套加密。

## 3. 同步什么

### 3.1 同步的记录（三种）

| 记录 | 内容 | 说明 |
|---|---|---|
| mount | `BundleMount` 全量（Mount 元数据 + Config + Credentials） | 与配置包 v1 里的同一结构；**只同步长效凭证**（RefreshToken、ClientSecret、AccessKey/SecretKey、MegaSession/MegaMasterKey），**AccessToken 不同步**——它是 1 小时左右的短效凭证，各设备用本地的 RefreshToken 现取（见 §7）。这样 token 刷新不再产生同步流量，竞态面也小得多；**本地文件夹挂载除外**（路径是设备相关的，见 §3.2） |
| job | `domain.SyncJob` 全量 | 用到本地文件夹做一侧的任务除外（与配置包导出规则一致） |
| settings | 设置 KV 的一个子集（见下） | 整个 settings 对象作为一条记录，LWW |

设置同步白名单（建议，待拍板）：**同步**主题、语言、预览缓存上限、退出时清理、默认排除规则；
**不同步**下载目录、代理（跟随系统/手动/直连）、开机启动——这些是设备相关的。
Agent 连接（地址 + API Token）**建议同步**（Token 按凭证处理，进加密记录），这样手机不用重新配
Agent 就能查运行记录；若用户不放心，改为不同步、每台设备单独配。

### 3.2 永远不同步的东西

- 本地文件夹挂载、含本地一侧的同步任务（对方没有这块磁盘）。
- 预览缓存（`zydrive-preview://` 缓存是本机临时文件）、传输队列与传输历史（内存/运行态）。
- 运行记录：住在 Agent 上，见 §1；桌面查 Agent 用现有 API，手机同理。
- 设备级状态：同步用的 deviceID、lastSeenRevision、脏记录队列（见 §6）。

### 3.3 载体（sync carrier）

- 用户在桌面选**一个已有的网盘**当载体（**默认建议 OneDrive**——用户 2026-11-27 定：
  不用苹果开发者账号、跨平台（Android 也能用）、OneDrive 驱动已真实实现、Graph 没有 Drime
  式的 ~15 小时 429 惩罚封禁、同步流量只有 KB 级；Drime 仍可作备选，但当载体时轮询必须克制，
  见 §6）。
- 载体上的固定目录：`ZyDrive/sync/`（挂载根下）。结构：

```text
ZyDrive/sync/
  manifest.json            # 清单（本身也是加密信封，见 §4）
  records/
    mount/<mount-id>.json  # 每条记录一个加密信封文件
    job/<job-id>.json
    settings.json          # 设置整包一条记录
```

- 文件名只用随机 ID，不含网盘名、任务名等可读信息；记录 ID 即 Mount/Job 的现有 ID
  （跨设备稳定，配置包重导就是按 ID 覆盖的）。
- 载体本身的凭证也在同步数据里（载体就是一个普通 mount 记录），自举没有循环问题：
  上传方已经有新凭证才上传；下载方用本地已有的旧凭证读载体，读到新记录后换上新凭证。
- 载体挂载被删/失效时，同步显式报错并引导重新选载体，不静默停摆。

## 4. 数据格式与加密

### 4.1 信封：复用配置包的信封格式

每条记录文件与 manifest 文件，都是 `bundleEnvelope` 同款结构
（`{version, salt, nonce, data}`，PBKDF2-SHA256 100k + AES-256-GCM），
只是 `data` 里装的不是整个 Bundle，而是一条记录或一份清单。
版本号升为 2（与 Bundle v1 区分，见 §8）。

### 4.2 清单（manifest）：加密的，不含任何明文元数据

```json
{
  "version": 2,
  "revision": 128,               // 单调递增，每次成功推送 +1
  "updatedAt": "2026-11-27T08:46:00Z",
  "records": [
    {"id": "m-...", "kind": "mount", "updatedAt": "...", "sha256": "…"},
    {"id": "j-...", "kind": "job",   "updatedAt": "...", "sha256": "…"},
    {"id": "settings", "kind": "settings", "updatedAt": "...", "sha256": "…"}
  ],
  "tombstones": [
    {"id": "m-...", "kind": "mount", "deletedAt": "..."}   // 保留 30 天后可清理
  ]
}
```

- 清单本身加密：载体厂商看不到网盘名、任务名、时间线，只能看到几个不透明文件。
  加密清单还能防伪造——没有同步口令就写不出一份能被解密的清单；
  攻击者只能删文件或重放旧文件（见 §9 的应对）。
- `sha256` 是对应记录文件解密后明文的哈希：下载方解密后验 hash，对不上就丢弃该条并报错，
  不让损坏/截断的记录进库。
- 清单里**不放**各设备的 lastSeen 之类状态，那是本地状态（§6）。

### 4.3 口令与密钥

- 同步口令：用户在桌面首次开启同步时设（≥8 位，与配置包口令同规则；
  建议提供"生成 24 位随机口令"选项，显示一次、让用户存好——与 Agent API Token 的做法一致）。
- 密钥派生：PBKDF2-SHA256 100k + 每文件独立 salt（与配置包完全一致）。
- 口令只在首次接入时输入一次，之后派生出的密钥存在本地 secrets store（本机 master.key 加密）。
  **口令本身不存**。忘掉口令的后果：已有设备继续同步不受影响（密钥还在），新设备加不进来；
  恢复办法是在一台已有设备上重置同步（重新生成口令并全量重传，旧记录作废）。
- 手机端：密钥进系统钥匙串能力（iOS Keychain / Android Keystore），不要放明文文件。
  mygo 侧有没有现成的安全存储接口，实现时核实；没有就先用 master.key 加密文件顶着。

## 5. 首次接入（bootstrap，仍需手动一次）

鸡生蛋问题：手机要知道去哪朵云上找同步数据，但"去哪朵云"这个信息本身就在同步数据里。
所以第一次仍走现成的手动导入，只是包里多两个同步字段：

1. 桌面：设置 → 同步 → 选载体 → 设同步口令 → **导出接入包**（Bundle v2，见 §8）。
   接入包 = 现有 Bundle v1 内容 + `sync{carrierMountID, }`；加密口令就是同步口令本身。
2. 用户把接入包传到手机（文件分享 / AirDrop / 二维码传包——三选一，仍是 `docs/mobile-app.md`
   里待定的传输方式；二维码只适合小包，接入包可能很大，首选文件分享/AirDrop）。
3. 手机：导入接入包（复用现有 `ImportBundle`，按 ID 装载 Mount/Job，凭证用本机 master.key
   重加密）→ 把同步口令派生的密钥存好 → 记下 `carrierMountID` → 立即做一次拉取。
4. 此后自动同步生效，不再手动。

第二台桌面接入同理。Agent 不用接入同步（Agent 的配置仍走现在的导出/导入；
Agent 是执行端不是编辑端，不参与合并）。

## 6. 同步流程

### 6.1 触发时机

- 启动时拉取一次；本地配置变更后**防抖推送**（如 2 秒内无新变更再推，避免每次按键都写云）。
- 打开状态下定期拉取：建议每 5 分钟一次（可配）。手机不做后台轮询（后台受限），
  只在打开 App 时拉取 + 变更后推送。
- 每次只 `Stat` 一下 manifest（1 次 API 调用）判断是否变化；没变化就什么都不做。
  Drime 上尤其要克制（429 惩罚封禁是 ~15 小时）。

### 6.2 拉取（pull）

1. 读本地 `lastSeenRevision`；Stat 载体上的 manifest，比对 revision（或内容 hash）。
2. 无变化 → 结束。有变化 → 下载并解密 manifest。
3. 对每条记录：`updatedAt` 比本地新 → 下载、解密、验 hash → 按 ID 写入本地库
   （Mount/Job 的 upsert 语义与 `ImportBundle` 一致，凭证用本机 master.key 重加密）。
4. 处理 tombstones：本地有对应记录且其 `updatedAt` 早于 `deletedAt` → 删除本地记录。
5. 更新 `lastSeenRevision`。拉取全程只读本地库、不改载体，失败可安全重试。

### 6.3 推送（push）

1. 本地变更先进**脏队列**（SQLite 持久化，崩溃不丢），而不是直接写云。
2. 推送时先读载体上的 manifest（拿最新 revision + 内容 hash）。
3. 上传脏记录文件（每条独立写，失败只重试该条），再写新 manifest（revision +1）。
4. 写 manifest 前**再读一次**：内容 hash 与第 2 步读到的不一致 → 说明有别的设备同时推了，
   合并对方的变更后重试（最多 3 次；超过则报"同步冲突，请重试"，不丢数据）。
   ——这是没有 CAS 机制的云盘上能做到的最接近原子的办法；两台设备恰好同时推的窗口很小，
   且冲突只会延迟同步，不会丢记录。
5. 推送成功后清脏队列，更新 `lastSeenRevision`。

### 6.4 合并规则（逐条 last-write-wins）

- 以记录的 `updatedAt` 为准（RFC3339，Mount/Job 已有）；新者胜。
- `updatedAt` 相等（同一秒内两边都改）→ 取 `deviceID` 字典序大者胜（确定性，不随机）。
- 删除（tombstone）与并发编辑：只看时间戳，`deletedAt` 更新者胜；tombstone 保留 30 天，
  过期后由任一设备在下次推送时清理。
- **设备时钟 skew**：`updatedAt` 取设备本地时间，手机/桌面时钟差几分钟就会误判。
  缓解：以载体文件的服务端 ModTime 为准做二次校验（Stat 返回的 ModTime 是服务端的）；
  若两条记录的 `updatedAt` 相差 < 5 分钟且分属不同设备，取服务端 ModTime 更新者胜。
  实现时以各家驱动 Stat 的 ModTime 可靠性为准（Drime/S3/Dropbox 都有服务端时间）。
- 本地独有记录（本地挂载等，§3.2）永不进入脏队列。

## 7. Token 刷新竞态（云同步本身不解决的部分）

**先说量：同步流量很小，不频繁。** 一条 mount 记录约 1–3 KB、一条 job 记录约 1 KB、
清单约 1 KB；打开状态下每 5 分钟只做 1 次 manifest Stat（每小时 12 次）；推送只在配置
变化或长效凭证轮换时发生。AccessToken（约 1 小时有效）**不同步**，各设备现取，
所以 token 刷新本身不产生同步流量。一天下来同步流量是 KB 级，OneDrive/Graph 的限流
在这个量级下不是问题。

问题：两台设备各拿一份 OAuth refresh token，各自独立刷新。部分厂商换 token 时会让旧的失效
（Google 在某些情况下轮换；Dropbox 的 refresh token 不轮换；Box/pCloud 换 token 要 Client Secret）。
若两边同时刷，一边拿到的新 token 会让另一边的旧 token 作废。

**设计（必须随同步一起做，不能留到以后）：**

1. 长效凭证轮换成功的一方立即把新凭证记入脏队列并推送（不等定期轮询），缩短窗口。
   （AccessToken 刷新不推送，各设备现取；只有 RefreshToken 等长效凭证轮换才推送。）
2. 刷新失败且错误为 `invalid_grant` / token 失效时，**不准拿旧 token 重试**：
   先从载体拉取最新记录，用拉到的新 token 重试一次；再失败才把该 Mount 标 `auth_expired`、
   提示用户重新授权（复用 v0.9.3 的 `auth_expired` 机制）。
3. 刷新路径必须 bump 对应 Mount 的 `updated_at`（见 §2 约束 2），否则别的设备看不到凭证变了。
4. 同一 Mount 的并发刷新：先拿到锁者胜——用 §6.3 的"写前再读"机制自然收敛：
   后写的一方会发现 manifest 已变，合并后用对方的新 token，不再用自己的旧 token 刷。

## 8. 版本与迁移

- **Bundle v2**：在 v1 结构上加 `sync{carrierMountID string, syncKeyHint string}`。
  `syncKeyHint` 只放口令提示（如"前 2 位 + 长度"），绝不放口令或密钥。
  v1 包仍可导入（无 sync 字段 = 不同步，行为与现在一致）。
- **本地迁移**（新表，两张）：
  - `sync_state`：`device_id`（每设备随机 UUID，首次生成）、`carrier_mount_id`、
    `last_seen_revision`、`last_sync_at`、`last_error`。
  - `sync_dirty`：脏队列（`record_id, kind, updated_at`），推送成功后删除。
- **后端新增**（都在 `internal/app`，复用现有分层；不碰 Provider 层）：
  - `internal/app/sync.go`：SyncEngine（pull/push/merge，调现有 `ListMounts`/`ImportBundle` 的
    底层能力，不直接调 Provider 之外的接口）。
  - `internal/app/bind.go`：新 Bind——`GetSyncStatus`、`EnableSync(carrierMountID, passphrase)`、
    `DisableSync()`、`SyncNow()`、`ExportJoinBundle()`（生成 v2 接入包）。
- **前端**：设置页新增"同步"卡片（状态、上次同步时间、立即同步、停用）；手机端同款迷你版
  （状态 + 立即同步 + 首次接入引导）。手机空态文案随之改（"从桌面导入接入包"而不是"添加网盘"）。

## 9. 安全分析（威胁模型）

| 攻击者能力 | 后果 | 应对 |
|---|---|---|
| 拿到载体上的全部文件 | 只能看到不透明密文与随机文件名；清单也加密，无网盘名/任务名/时间线可读 | §4：清单与记录全部 AES-256-GCM 加密 |
| 离线暴力破解同步口令 | 口令 ≥8 位 + PBKDF2 100k；弱口令仍可被破 | 口令强度提示 + 推荐生成式 24 位随机口令；口令只在设备间口头/面对面传递一次 |
| 篡改记录文件 | 解密验 hash 失败，该条被丢弃并报错，不进库 | §4.2 `sha256` 校验 |
| 伪造清单/记录 | 没有同步口令就写不出能解密的清单 | 清单加密（§4.2）；攻击者能删文件、重放旧文件，但伪造不了 |
| 删光载体上的文件 / 重放旧清单 | 同步停摆或看到旧数据；**本地数据不受影响**（同步是合并，各设备本地库完整） | 删文件 → 显式报错；重放旧清单 → 设备发现 revision 回退，拒绝回退并报警，之后的新推送自愈 |
| 载体账号本身被盗 | 攻击者能删同步数据（DoS），但解不出凭证 | 同上；且同步数据只是配置副本，不是唯一份 |
| 手机丢失 | 捡到者进不了 App 则拿不到密钥；App 无锁屏则与手机丢失的一般风险相同 | 密钥进系统钥匙串；建议用户开设备锁屏（应用内不再另加密） |

**不承诺的**：同步口令忘掉后无法从任何地方找回（没有服务器可重置），只能在一台已有设备上
重置同步（§4.3）。

## 10. 实施顺序（建议）

1. **Bundle v2 + 接入包导出/导入**：复用现有 `EncryptBundle` / `ImportBundle`，加 sync 字段；
   桌面能导出、手机能导入，手动链路先通（这一步不写云，风险最小）。
2. **载体读写 + 拉取**：`SyncEngine` 的 pull、manifest 解析、逐条 upsert；桌面先上。
3. **推送 + 合并 + 脏队列**：push、写前再读、tombstone、时钟 skew 缓解。
4. **Token 刷新竞态**：§7 的三条（立即推送、失效先拉后重试、刷新 bump `updated_at`）。
5. **手机端**：接入引导、同步状态行、立即同步；空态文案更新。
6. **收尾**：载体被删/失效的引导、同步冲突提示、设置白名单开关、文档（更新 `docs/mobile-app.md`
   §一与 `docs/agent-deploy.md` 的相关段落）。

## 11. 待拍板问题

1. **载体默认选哪朵云？** ~~建议默认 Drime（用户主存储）~~ → **用户 2026-11-27 定：默认 OneDrive**
   （不用苹果开发者账号、跨平台、驱动现成、无 429 惩罚封禁）。Drime 仍可作备选。
2. **同步口令**：用户自设，还是默认生成 24 位随机口令（显示一次）？
3. **设置白名单**：§3.1 的划分是否接受？Agent 连接同步还是每台设备单独配？
4. **同步是免费还是 Pro？** 建议免费——同步走用户自己的云、无我们的服务器成本；
   但它是多设备能力，与 Pro 的"多设备激活"叙事有关，需与 `docs/monetization.md` 的口径对齐。
5. **轮询间隔**：默认 5 分钟是否接受？Drime 载体下每次轮询只有 1 次 Stat 调用。
6. **手机端首次接入的传输方式**：文件分享 / AirDrop / 二维码，三选一（二维码只适合小包，
   接入包大时不适用）。
