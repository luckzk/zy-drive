# 各网盘要做的功能清单（草稿，2026-11-26）

依据：`docs/provider-capabilities.md`（2026-11-26 对比，五家）+ 本日对
9 家驱动 `Caps()` 与已实现方法的逐项核对（`internal/storage/providers/*/*.go`、
`internal/storage/provider.go`、`internal/app/transfers.go`）。
范围：Box、Dropbox、Google Drive、Drime、Local、MEGA、OneDrive、pCloud、S3/R2。
不在本文范围：OAuth 与添加向导（见 `docs/oauth-comparison.md` /
`docs/add-mount-comparison.md`）、客户端加密（`docs/client-encryption.md`）。

核对方法说明：每项“已实现 / 未实现”以代码里有没有对应方法为准，
不以 Caps 标志为准——因为本次核对发现 Caps 有多处与实现不符（见 §0）。
真账号状态按 2026-11-26 已知情况写；未在真账号上跑过的项标“待真账号验证”。

---

## §0 先做：三件跨网盘的收敛工作（不分网盘，做完再逐家补功能）

这三件是 `provider-capabilities.md` §四的落地，做完后每家清单里的
“Caps 改错”项自动消失；不做的话，逐家清单里的每一项都会反复撞上同一个坑。

### 0.1 Caps 只能由已实现的方法推导 + 测试断言（P0）

现状（已核实）：

- `SupportsVersions=true` 写在 Drive、S3、Dropbox 的 Caps 里，但
  `Versions()/Restore()` 只在 OneDrive 实现（`onedrive.go:1148/1182`），
  其余三家没有对应方法。
- `SupportsShare=true` 写在 S3 的 Caps 里，但 S3 没有 `Share()` 方法，
  预签名 URL 走的是另一个接口 `DirectURLer`（只供预览）。
- `SupportsResumableUpload=true` 写在 Drive、Dropbox、OneDrive、S3 的 Caps 里，
  但 `ResumableUploader`（`ResumeState` / `UploadResumable`）只在 Drime 实现
  （`drime/resume.go:170/191`）。OneDrive 自己的注释写明了
  “upload sessions; resume across restarts is not implemented”。
- `SupportsServerSideCopy` 在 S3、Dropbox 是 true，但全仓库根本没有
  对应的可选接口（`provider.go` 里没有 `Copier`），应用层没有任何代码
  能调用服务端复制；跨盘复制实际走 OpenRead → Upload 流式。

要做：

1. 在 `internal/storage/contracttest` 加断言：每个驱动的 Caps 标志
   与其实现的可选接口一一对应（有方法 ⇔ 标志 true）。
   覆盖：`Searcher`、`DeltaSource`、`Sharer`、`Versioner`、`Thumber`、
   `QuotaSource`、`DirectURLer`、`ResumableUploader`。
2. 在断言通过之前，把下面四处 Caps 先改回 false（或把功能真做出来，
   二选一；建议先改 false，功能按各家清单排期做）：
   - Drive / S3 / Dropbox 的 `SupportsVersions` → false；
   - S3 的 `SupportsShare` → false（或拆独立标志“预签名直链（仅预览）”）；
   - Drive / Dropbox / OneDrive / S3 的 `SupportsResumableUpload` → false；
   - S3 / Dropbox 的 `SupportsServerSideCopy` → false，
     或新增 `Copier` 接口并把 S3 `CopyObject` / Dropbox `copy_v2` 接上去。
3. `Sharer` 是唯一有接口、但应用层没有任何代码做类型断言的能力
   （全仓库搜不到 `Sharer` 的使用处）：分享、版本这两个后端已实现的能力，
   界面至今没接线（检查器文案写“尚未接入”）。接线时按
   `provider-capabilities.md` §四.3：UI 只读 Caps 决定显隐，
   执行前再断言接口，断言失败报“该网盘不支持”。

### 0.2 `ShareLink` 统一改名 `Share`（P1）

现状（已核实）：`Share()`（`Sharer` 接口）在 OneDrive / Drive / Drime /
Dropbox 实现；`ShareLink()`（同名不同方法）在 Box（`box.go:627`）、
MEGA（`mega.go:455`）、pCloud（`pcloud.go:893`）实现。
`provider.go` 里没有 `ShareLink` 这个接口，应用层认不出后三家，
即使实现了也进不了统一入口。

要做：三家把 `ShareLink` 改名 `Share`，实现 `Sharer` 接口；
改完跑一遍契约测试，确认 Caps 的 `SupportsShare=true` 与接口对应。

### 0.3 检查器“高级”从能力名清单改成功能入口（P2）

现状：检查器高级页列出的能力名里有实现并不存在的项（见 0.1），
而分享、版本两个后端已实现的能力在界面上完全没接线。
要做：0.1 的断言通过后，检查器只列断言通过的能力；分享、版本
接线后，高级页的对应项从文案变成可点的入口。

---

## OneDrive（最完整的一家，对照基准）

现状：9 家里唯一实现全部可选接口的驱动——Search、Delta、Share、
Versions + Restore、Thumbnail、Quota、DirectURL 全有；Caps 与实现一致。
唯一填了 `IllegalChars` 与 `ReservedNames` 的一家。
真账号状态：mock 测试，未在真实账号上端到端跑过。

- [ ] P1 跨重启断点续传：上传会话已实现，但注释写明跨重启续传未实现；
  要么按 Drime 的做法把 uploadId + 已完成片持久化到 SQLite，
  要么把 `SupportsResumableUpload` 改回 false（见 0.1，先改 false 再排期做）。
- [ ] P1 容量/配额以外的字段补齐：`createdDateTime` 目前只作修改时间
  fallback，接入为独立的创建时间字段；`webUrl` 接入 Node（官方链接）。
- [ ] P2 分享、版本接 UI：后端 `Share` / `Versions` / `Restore` 已实现，
  接线到文件右键菜单与检查器高级页（依赖 0.3）。
- [ ] P2 真账号验证：List / Stat / Upload / Rename / Delete 全流程 +
  容量接口 + 上传会话 + 版本恢复，在真实账号上跑一遍。
- [ ] P3 校验值：`quickXorHash` 不可跨云比，在 `HashKind` 约定里登记
  （与 §0 的去重规则一起落到代码，不只写注释）。

## Google Drive

现状：Search、Delta、Share、Thumbnail、Quota 已实现；Caps 与实现一致，
除 `SupportsVersions=true` 是错的（无 `Versions()/Restore()`）。
Ref 是 ID 型，显示路径靠 parents 查询重建。
真账号状态：mock 测试，未在真实账号上端到端跑过。

- [ ] P0 Caps 改错：`SupportsVersions` 改 false，或把 revisions 接出来
  （API 有 revisions，未接入）。二选一；建议先改 false。
- [ ] P1 字段补齐：`fileFields` 加 `createdTime`（创建时间）；
  `trashed` 已请求但未接入 Node，接入或去掉请求；
  `owners` / `shared` / `webViewLink`（已请求，未接入 Node）接入 Node。
- [ ] P1 版本功能（二选一，与 P0 联动）：实现 `Versions()/Restore()`，
  把 Caps 的 true 变成真的；或保持 false，版本功能不做。
- [ ] P1 分享接 UI：后端 `Share` 已实现，接线到右键菜单与检查器（依赖 0.3）。
- [ ] P2 服务端复制语义：`files.copy` 存在但语义与 S3/Dropbox 的同盘复制不同，
  Caps 写 false 是对的；跨盘复制继续走流式，不用改，只需在文档里写明。
- [ ] P2 真账号验证：全流程 + 容量 + Delta（changes.list）+ 搜索，
  在真实账号上跑一遍。
- [ ] P3 校验值：md5 登记进 `HashKind` 约定（能否跨云比较）。

## Dropbox

现状：Search、Delta、Share、DirectURL、Thumbnail、Quota 已实现；
`copy_v2` 能力在 Caps 里（`SupportsServerSideCopy=true`），但无 `Copier` 接口。
Caps 两处错：`SupportsVersions=true`（无实现）、
`SupportsResumableUpload=true`（无 `ResumableUploader` 实现）。
真账号状态：mock 测试，未在真实账号上端到端跑过；
另有用户待办：Dropbox App Console 勾选 `account_info.read` 后重新授权
（旧 Token 拿不到新 scope）。

- [ ] P0 Caps 改错：`SupportsVersions`、`SupportsResumableUpload` 改 false，
  或把对应功能真做出来；`SupportsServerSideCopy` 按 0.1 处理。
- [ ] P1 字段补齐：`client_modified` 接入为创建时间；
  `sharing_info` 接入 Node；`content_hash` 登记方言（4MB 分块 SHA-256 串联，
  Dropbox 内部专用，不可跨云比）。
- [ ] P1 版本功能（二选一）：实现 `Versions()/Restore()`（API 有 revisions），
  或保持 Caps false。
- [ ] P1 分享接 UI：后端 `Share`（`create_shared_link_with_settings`）已实现，
  接线到右键菜单与检查器（依赖 0.3）。
- [ ] P2 断点续传：上传会话是 4MB 片；要跨重启续传需持久化会话，
  否则 Caps 保持 false。
- [ ] P2 真账号验证：全流程 + 重新授权后的容量接口（用户须先在
  App Console 勾选 `account_info.read` 并重新授权），在真实个人账号上跑一遍。

## S3 / R2

现状：核心 8 方法全有；`DirectURL`（预签名）已实现；`Quota` 无此 API
（UI 显示“不限量”，正确）。Caps 三处错：`SupportsVersions=true`、
`SupportsShare=true`（无 `Share()` 方法）、`SupportsResumableUpload=true`
（无 `ResumableUploader` 实现）。`SupportsServerSideCopy=true` 但无接口。
真账号状态：**唯一在真实账号上端到端跑过的云**（R2 测试桶）；
但 transfermanager 迁移（v0.11.0 后的 lint 批）后真机 R2 上传待 Mac 实测。

- [ ] P0 文件夹删除补展开（已确认的缺口）：`DeleteNodes` 按 ref 逐个调
  `Delete`，不展开文件夹，S3 上只删掉占位对象、内容残留。
  修法：`DeleteNodes` 遇到文件夹先 List 展开再删，或 S3 驱动内按前缀批量删；
  修完补测试（`provider-capabilities.md` §四.4）。
- [ ] P0 Caps 改错：`SupportsVersions`、`SupportsShare`、
  `SupportsResumableUpload`、`SupportsServerSideCopy` 按 0.1 处理
  （Share 改述为“预签名直链（仅预览）”或拆独立标志）。
- [ ] P1 版本功能（二选一）：实现 `Versions()/Restore()`（API 有 versionId，
  需桶开启版本控制），或保持 Caps false。
- [ ] P1 字段补齐：`StorageClass` 接入 Node；SSE / SSE-KMS 在上传参数里
  已有，接入 Caps/设置页的可见项；版本控制开启状态在挂载时提示。
- [ ] P1 断点续传：multipart 是 5MB 片（transfermanager 迁移后切分点不变）；
  要跨重启续传需持久化 multipart uploadId，否则 Caps 保持 false。
- [ ] P2 搜索与增量：无服务端搜索、无 delta 接口，现状是全量扫描 +
  ETag 比对 + 本地文件名索引；不做服务端搜索，只需在文档里写明，
  并确认本地索引覆盖 S3 挂载。
- [ ] P2 真账号验证：transfermanager 迁移后，在 Mac 上实际传一次文件
  （lint 批的已知缺口）；文件夹删除修复后，在 R2 测试桶上复测。

## Drime

现状：Search、Share、Thumbnail、Quota、DirectURL（预签名）已实现；
断点续传是 9 家里唯一完整实现的（uploadId + 已完成片持久化 SQLite，
跨重启续传，v0.9.1）。Caps 与实现一致。无 delta 接口（全量扫描），
无版本 API。真账号状态：真实账号跑通过（上传/下载经 aigo 流水线实测）。

- [ ] P1 字段补齐：`created_at` 接入为创建时间；`url` 接入 Node（官方链接）；
  `file_hash` 方言登记（SHA-256 64 位 / MD5 32 位才认；`hash` 字段是
  URL 安全 ID，不是内容哈希）。
- [ ] P1 分享接 UI：后端 `Share` 已实现，接线到右键菜单与检查器（依赖 0.3）。
- [ ] P2 搜索分页：`file-entries?query=` 分页 50 条，确认大结果集翻页不断；
  搜索结果的路径重建（爬 parent_id）与 Drive 的 parents 查询方式
  对照收敛（`provider-capabilities.md` §三.3）。
- [ ] P2 增量：无 delta 接口，现状全量扫描；不做接口，只需确认同步任务
  在 Drime 挂载上的全量比对性能可接受。
- [ ] P3 校验值：`file_hash` 登记进 `HashKind` 约定（能否跨云比较）。

## MEGA

现状：Search（客户端树遍历，非服务端搜索）、Quota 已实现；
`ShareLink` 已实现但未进统一入口（见 0.2）。Caps 与实现一致，
除 `SupportsShare=true` 实际走的是 `ShareLink`。
客户端加密是 9 家里唯一 `SupportsEncryption=true` 的一家
（MEGA 设计上全程客户端加密）。
真账号状态：本 VM 能连；2026-11-26 实测密码登录第一步即被拒
（错误 -16 账号被锁，非密码错），用户重置密码后恢复，旧会话继续有效；
挂载加载失败的根因已确认为账号被锁，非代码 bug。

- [ ] P1 `ShareLink` 改名 `Share`（见 0.2），进统一入口后接 UI。
- [ ] P1 搜索语义：`SupportsSearch=true` 但实现是客户端树遍历；
  在 Caps/文档里注明“客户端遍历”，与服务端搜索区分开，
  或把标志拆成“服务端搜索 / 本地遍历”两档。
- [ ] P2 缩略图：`SupportsThumbnail=false`；API 有无缩略图接口待查，
  有则接 `Thumber`，无则保持 false 并在文档里写明。
- [ ] P2 断点续传：分片上传已有，但会话未持久化，失败从零开始；
  要跨重启续传需持久化会话，否则 Caps 保持 false。
- [ ] P2 真账号验证：解锁后旧会话已恢复；在真实账号上跑全流程 +
  大文件上传，确认会话过期分类（v0.9.3 的 `ErrSessionExpired`）
  只在服务器拒会话时触发，瞬时失败不标过期。

## Box

现状：Search、Quota 已实现；`ShareLink` 已实现但未进统一入口（见 0.2）。
无 Delta、无 Thumbnail、无版本。上传会话存在但不持久化/不续传
（Caps 诚实地写了 false）。真账号状态：mock 测试；无官方应用，
一键授权显示“还没配置”，须用自建应用，注册指南已写。

- [ ] P1 `ShareLink` 改名 `Share`（见 0.2），进统一入口后接 UI。
- [ ] P1 授权：按 `docs/oauth-comparison.md` 的 P1/P2 走——内置应用
  转生产或走自建应用引导；一键授权目前不可用是已知缺口。
- [ ] P2 缩略图：`SupportsThumbnail=false`；API 有无缩略图接口待查，
  有则接，无则保持。
- [ ] P2 增量：无 delta 接口；现状全量扫描，确认同步任务性能可接受，
  或接 Box 的事件接口（待评估）。
- [ ] P2 断点续传：上传会话不持久化；要跨重启续传需持久化，
  否则 Caps 保持 false。
- [ ] P3 真账号验证：自建应用授权跑通后，全流程跑一遍。

## pCloud

现状：Search、Quota、DirectURL（`getfilelink`）已实现；
`ShareLink` 已实现但未进统一入口（见 0.2）。无 Delta、无 Thumbnail、
无版本。真账号状态：mock 测试；无官方应用，一键授权显示“还没配置”，
须用自建应用；欧洲区换 Token 端点没在真实欧洲账号上验过；
lint 批刚修了一个真 bug：小文件上传 `err` 被分支内同名遮住、
失败也返回成功（已修 + 回归测试）。

- [ ] P1 `ShareLink` 改名 `Share`（见 0.2），进统一入口后接 UI。
- [ ] P1 授权：同 Box，按 `docs/oauth-comparison.md` 走自建应用引导。
- [ ] P2 缩略图：`SupportsThumbnail=false`；API 有无缩略图接口待查。
- [ ] P2 增量与断点续传：均无；要做需评估 pCloud 的对应接口，
  否则保持 false 并在文档里写明。
- [ ] P2 真账号验证：自建应用授权跑通后，全流程跑一遍；
  欧洲区账号换 Token 端点单独验证一次。

## Local（本地文件夹）

现状：核心 8 方法全有；`Quota` 已实现（磁盘用量）；
其余可选接口全是 false，Caps 与实现一致。删除是永久删除
（`DeleteToTrash=false`），无回收站。

- [ ] P1 删除二次确认文案：本地删除是永久删除，确认框须写清楚
  “永久删除，不进回收站”，与进回收站的云区分开。
- [ ] P2 搜索：`SupportsSearch=false`；本地文件夹走本地文件名索引
  即可，确认索引覆盖本地挂载；不做服务端搜索。
- [ ] P2 断点续传：本地复制不需要断点续传，保持 false，
  在文档里写明原因即可。
- [ ] P3 真账号验证：本地文件夹是本机路径，无需账号验证；
  确认 Agent 端选本地文件夹时按设计提示/置灰（本地只能本机）。

---

## 排期建议（按 §0 → 各家 P0 → 各家 P1 的顺序）

1. §0 三件收敛（断言 + 改名 + 检查器入口）——做完后 Caps 不再说谎，
   后面每家的工作量都变小。
2. S3 文件夹删除补展开（唯一已确认的数据缺口：删文件夹留内容）。
3. 各家 P1：字段补齐（创建时间/官方链接/拥有者）+ 分享、版本接 UI
   （OneDrive / Drive / Drime / Dropbox 的后端已实现，只差接线）。
4. 各家 P2：断点续传按 Drime 的做法逐家补（OneDrive / Dropbox / S3 /
   MEGA / Box / pCloud），或 Caps 保持 false。
5. 各家真账号验证：R2 已跑通；Dropbox / Drive / OneDrive / Drime /
   MEGA / Box / pCloud 按“自建应用 / 重新授权 / 解锁恢复”等前置条件
   逐家跑，跑通一项勾一项。

## 待用户拍板（本文不替你定）

- Drive / S3 / Dropbox 的版本功能：是把 `Versions()/Restore()` 真做出来，
  还是把 Caps 改回 false、版本功能不做？
- S3 / Dropbox 的服务端复制：是新增 `Copier` 接口接上去，还是把
  `SupportsServerSideCopy` 改回 false、继续走流式复制？
- 断点续传：除 Drime 外的六家，是按 Drime 的做法逐家补持久化，
  还是接受“会话内续传、无跨重启续传”，Caps 保持 false？
