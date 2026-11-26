# 各网盘文件字段与操作对比（2026-11-26）

调查范围：OneDrive、Google Drive、S3/R2、Drime、Dropbox 五家已接入驱动。
依据：`internal/storage/providers/<各家>/*.go` 的实际代码（Caps、Node 映射、
已实现的方法）+ `internal/storage/provider.go` 的统一接口
+ `internal/domain/caps.go` + 前端 `Inspector.tsx` / `FileBrowser.tsx`。
API 本身有、驱动没接的字段也列出（“API 有 / 未接入”）。

统一模型：`domain.Node` 只有 Ref / Path / Name / IsDir / Size / ModTime /
Hash + HashKind / Mime / ThumbURL / Extra（各家自定键，通用代码不许读）。
`domain.Caps` 是能力表，UI 只读它决定显隐。

## 一、文件字段：List / Stat 能拿到什么

| 字段 | OneDrive | Google Drive | S3 / R2 | Drime | Dropbox |
|---|---|---|---|---|---|
| 名称 | ✓ | ✓ | ✓（key 末段） | ✓ | ✓ |
| 大小 | ✓（文件；文件夹 -1） | ✓（文件；文件夹 -1） | ✓（文件；文件夹 -1） | ✓（file_size；文件夹 -1） | ✓（文件；文件夹 -1） |
| 修改时间 | ✓ lastModifiedDateTime（解析失败才用 createdDateTime） | ✓ modifiedTime | ✓ LastModified | ✓ updated_at | ✓ server_modified（client_modified 未接入） |
| 创建时间 | API 有，未接入（只作修改时间 fallback） | API 有，未接入（fileFields 没要 createdTime） | —（对象无创建时间，只有 LastModified） | API 有 created_at，未接入 | API 有 client_modified，未接入 |
| 类型 MIME | ✓ file.mimeType | ✓ mimeType | List 按扩展名猜，Stat 用 ContentType | ✓ mime（空时按扩展名猜） | ✗ API 不返回，按扩展名猜 |
| 内容校验值 | sha1（仅有时有；quickXorHash 不可跨云比） | md5 | ETag（单段=MD5，多段不可跨云比） | file_hash（SHA-256 64 位 / MD5 32 位才认；`hash` 字段是 URL 安全 ID，不是内容哈希） | content_hash（4MB 分块 SHA-256 串联，Dropbox 内部专用） |
| 缩略图 | ✓（Thumbnail 接口，Graph 缩略图） | ✓ thumbnailLink（List 时直接带） | ✗ 无 | ✓ thumbnail_url（List 时直接带） | ✓（Thumbnail 接口） |
| 官方链接 | API 有 webUrl，未接入 | API 有 webViewLink（已请求，未接入 Node） | —（预签名链接走 DirectURL，供预览用） | API 有 url，未接入 | —（分享链接另走 Share） |
| 共享状态 / 权限 | API 有，未接入 | API 有 owners / shared，未接入 | —（桶策略，不属文件字段） | API 未提供 | API 有 sharing_info，未接入 |
| 拥有者 / 创建者 | API 有，未接入 | API 有 owners，未接入 | — | — | — |
| 版本信息 | ✓（Versions()，版本 ID 放 Extra[versionId]） | API 有 revisions，未接入 | API 有 versionId（需开版本控制），未接入 | API 无版本 | API 有 revisions，未接入 |
| 存储类型 / 加密 | — | — | API 有 StorageClass，未接入 Node；SSE/SSE-KMS 在上传参数里 | — | — |
| 回收站状态 | 已删除项打 Extra[removed]=true（Delta 用） | trashed 已请求但未接入 Node（List 用 `trashed=false` 过滤） | —（无回收站，删除即永久） | — | —（删除即进回收站） |
| Ref 语义 | 路径（Extra 存 onedriveId） | ID（Ref=文件 ID；显示路径靠 parents 查询重建） | 路径（key，去掉桶前缀） | 路径（Extra 存 drimeId 整数 id；搜索结果靠 rebuildPath 爬 parent_id 重建） | 路径（path_display） |

## 二、文件操作：驱动实现了什么

| 操作 | OneDrive | Google Drive | S3 / R2 | Drime | Dropbox |
|---|---|---|---|---|---|
| 浏览 List | ✓（Graph children） | ✓（files.list，`in parents`） | ✓ ListObjectsV2 | ✓ file-entries | ✓ list_folder |
| 单个元数据 Stat | ✓ | ✓ | ✓ HeadObject | ✓ | ✓ |
| 下载 OpenRead | ✓（downloadUrl 直链，供预览 DirectURL） | ✓（流式；无 DirectURL，走代理） | ✓（预签名直链，供预览 DirectURL） | ✓（走代理；无 DirectURL） | ✓（临时链接直链，供预览 DirectURL） |
| 上传 Upload | ✓ 单段 / 上传会话分片（5MB 片；跨重启续传未实现） | ✓ 单段 / 可续传上传（8MB 片） | ✓ 单段 / multipart（5MB 片） | ✓ 单段 / multipart（5MB 片；uploadId+已完成片持久化 SQLite，跨重启续传，v0.9.1） | ✓ 单段 / 上传会话（4MB 片） |
| 新建文件夹 Mkdir | ✓ | ✓ | ✓（零字节占位对象 + `/`） | ✓ | ✓ |
| 重命名 / 同盘移动 Rename | ✓ PATCH 改名（含移目录） | ✓ PATCH 改名/移目录 | ✓ CopyObject + DeleteObject（非原子） | ✓ POST 改名接口 | ✓ move_v2 |
| 删除 Delete | ✓ 进回收站 | ✓ 移入回收站（files.update trashed=true） | ✓ 永久删除（单个对象；文件夹只删占位对象，内容残留——见 §三.5） | ✓ 进回收站 | ✓ delete_v2，进回收站 |
| 跨盘移动 MoveNodes | ✓（复制+删源；复制失败绝不删源，v0.10.0） | ✓ 同左 | ✓ 同左 | ✓ 同左 | ✓ 同左 |
| 服务端复制 | ✗（Caps false） | ✗（files.copy 存在但语义不同，Caps false；跨盘复制走流式） | ✓ CopyObject | ✗（Caps false） | ✓ copy_v2 |
| 服务端搜索 Search | ✓（Graph search，KQL） | ✓（files.list q=） | ✗（Caps false；走本地文件名索引） | ✓（file-entries?query=，分页 50 条） | ✓（search_v2） |
| 增量同步 Delta | ✓（/delta） | ✓（changes.list） | ✗（全量扫描 + ETag 比对） | ✗（无 delta 接口；全量） | ✓（list_folder cursor + longpoll） |
| 分享链接 Share | ✓ 已实现（Graph 创建分享链接）；UI 未接线 | ✓ 已实现；UI 未接线 | ✗ 无 Share 方法（预签名 URL 走 DirectURL，仅供预览） | ✓ 已实现；UI 未接线 | ✓ 已实现（create_shared_link_with_settings）；UI 未接线 |
| 历史版本 Versions / 恢复 Restore | ✓ 已实现（唯一一家） | ✗ 未实现（但 Caps 写 SupportsVersions=true，见 §三.1） | ✗ 未实现（但 Caps 写 true；版本控制本身需桶开启） | ✗ API 无版本（Caps false，一致） | ✗ 未实现（但 Caps 写 true） |
| 容量 Quota | ✓ | ✓ | ✗ 无此 API（UI 显示“不限量”） | ✓（按工作区，used+available） | ✓ |
| 单文件上限 | 250GB | 5TB | 5TB | 无文档上限（Caps 0） | 2TB（上传会话） |
| 分片大小 | 5MB | 8MB | 5MB | 5MB | 4MB |
| 路径长度上限 | 400 字符 | 无文档硬上限（Caps 0） | 无文档硬上限 | 文件名 255 字节 UTF-8 | 无文档硬上限 |
| 文件名非法字符 | `"/\:*?<>|` + 保留名 CON/PRN/AUX/NUL/COM1-9/LPT1-9 | —（Caps 未填） | —（Caps 未填） | —（Caps 未填） | —（Caps 未填） |

## 三、乱在哪（本次调查确认的七处不一致）

1. **Caps 与实现对不上。** `SupportsVersions=true` 写在 Drive、S3、Dropbox 的 Caps 里，
   但 `Versions()/Restore()` 只在 OneDrive 实现。检查器照 Caps 列出“支持历史版本”，
   后面却什么都没有。S3 的 `SupportsShare=true` 同理：没有 `Share()` 方法，
   预签名 URL 走的是另一个接口 `DirectURLer`（只供预览用）。
2. **分享两个名字。** `Share()`（Sharer 接口：OneDrive/Drive/Drime/Dropbox）
   与 `ShareLink()`（Box/MEGA/pCloud 的同名不同方法）并存；应用层只用类型断言
   认前者，后三家即使实现了也进不了统一入口。
3. **Ref 两套语义。** 路径型（Dropbox/S3/Drime）vs ID 型（Drive/OneDrive），
   显示路径重建方式各写各的：Drime 爬 parent_id、Drive 查 parents、OneDrive 用 path。
4. **校验值五种方言。** content_hash / md5 / sha1 / ETag / file_hash，
   只在代码注释里写明“不可跨云比较”；`Node.HashKind` 标了算法，但没有统一的
   “能否比较”规则落到代码里（去重设计文档里有，代码里没有）。
5. **删除语义分散。** 回收站 vs 永久只有一个 Caps 布尔值；S3 文件夹删除是
   已确认的缺口：`DeleteNodes` 按 ref 逐个调 `Delete`，不展开文件夹，
   S3 上只删掉占位对象、内容残留（其余四家服务端递归删除）。
6. **Extra 是收容区。** 键名各家自定（onedriveId / drimeId / drimeHash / versionId），
   通用代码不许读，但版本恢复又依赖 Extra 里的 versionId。
7. **检查器“高级”是能力名清单，不是功能入口。** 分享、版本两个后端已实现的能力，
   界面上完全没接线（文案写“尚未接入”）；与此同时检查器列出的能力名里有
   实现并不存在的项（见 1）。

## 四、收敛办法（建议顺序）

1. **一张表定生死。** Caps 的每个 `Supports*` 只能由“已实现对应接口方法”推导，
   不许手写 aspirational 的 true。加测试断言：每个驱动的 Caps 与其实现的
   可选接口一一对应（有方法 ⇔ 标志 true）。
2. **先修三处不一致，再接新功能。** Drive/S3/Dropbox 的 `SupportsVersions`
   改回 false（或把 Versions/Restore 真做出来）；S3 的 `SupportsShare` 改述为
   “预签名直链（仅预览）”或拆出独立标志；`ShareLink` 统一改名 `Share`。
3. **操作入口统一走两层。** UI 只读 Caps 决定显隐；执行前再类型断言接口，
   断言失败报“该网盘不支持”，绝不出现灰掉的死按钮（已有规则，坚持住）。
4. **S3 文件夹删除补展开。** `DeleteNodes` 遇到文件夹先 List 展开再删，
   或 S3 驱动内按前缀批量删；修完补测试。

## 五、新网盘接入清单（后续加网盘时逐项打勾）

- [ ] 核心 8 方法：List / Stat / OpenRead / Upload / Mkdir / Rename / Delete / Caps。
- [ ] Caps 只写已实现的项；Limits 填实测值（单文件上限、分片、路径上限、非法字符）。
- [ ] Node 映射表：在 §一 里加一列——每个字段填“API 字段名 / 未接入原因”。
- [ ] 校验值方言登记：算法名 + 是否内容哈希 + 能否跨云比较（写进 `HashKind` 约定）。
- [ ] 删除语义登记：回收站 / 永久；文件夹是否服务端递归；S3 式占位对象要不要特殊处理。
- [ ] 四选一：搜索 / 增量 / 分享 / 版本——实现哪个就接哪个接口，UI 自动显隐。
- [ ] 真账号跑通：List / Stat / Upload / Rename / Delete 全流程 + 容量接口。
