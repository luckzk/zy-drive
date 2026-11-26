# Alist（OpenList）跨盘搜索实现分析

> 分析对象：`github.com/OpenListTeam/OpenList`（module `v4`），2026-11-25 shallow clone（`--depth 1`，main 分支）到 `~/workspace/tmp/alist`。
> 方法：只读通读关键代码，不运行、不构建。以下所有路径、函数名均以该快照为准；引用的行号级细节以“文件 + 函数”定位，不贴大段代码。
> 一句话结论：**Alist 的搜索只有文件名索引，没有内容搜索**；它是“本地索引 + 可插拔后端”架构，默认关闭，四个后端共用一个 `Searcher` 接口；最值得抄的是接口形状、差分式增量更新和路径哈希技巧，最该避的是 bleve 后端与 SQLite 锁库、中文分词这两处坑。

---

## 1. 总览：架构与调用流程

### 1.1 分层

```
HTTP 层                    server/handles/search.go        Search()
                           server/handles/index.go         BuildIndex / UpdateIndex / StopIndex / ClearIndex / GetProgress
                           server/middlewares/search.go    SearchIndex（模式为 none 时 404）
                                     │
门面层                     internal/search/search.go       单例 instance + Init(mode) + SearchFiltered()
                           internal/search/build.go        BuildIndex() / Update() / Del() / Clear() / Config()
                           internal/search/util.go         Progress() / WriteProgress() / updateIgnorePaths()
                           internal/search/update_lock.go  per-parent 锁
                                     │
接口层                     internal/search/searcher/searcher.go   Searcher / FilteredSearcher / Config
                           internal/search/searcher/manage.go    NewMap 注册表
                                     │
后端层                     internal/search/db/                 database（全文）
                           internal/search/db_non_full_text/   database_non_full_text（LIKE）
                           internal/search/bleve/              bleve（内嵌全文引擎）
                           internal/search/meilisearch/        meilisearch（外部服务）
                                     │
存储层                     internal/db/searchnode.go           search_nodes 表（gorm）
                           各后端自有存储（bleve 目录 / Meilisearch 服务）
```

### 1.2 模式切换

- 设置项 `search_index`（`internal/conf/const.go:75`，默认值见 `internal/bootstrap/data/setting.go:202`）：**默认 `"none"`，即搜索默认关闭**，可选 `database` / `database_non_full_text` / `bleve` / `meilisearch` / `none`。
- `search.Init(mode)`（`internal/search/search.go`）由设置项 hook 触发（`op.RegisterSettingItemHook(conf.SearchIndex, …)`）：模式不变则什么都不做；切换时先 `Release()` 旧后端，再从 `searcher.NewMap[mode]` 构造新后端；建索引进行中时拒绝切换（`Running()` 检查，错误 `index is running`）。
- 中间件 `server/middlewares/search.go:SearchIndex`：模式为 `none` 时搜索与索引管理接口直接返回 404 `search not available`（`internal/errs/search.go:SearchNotAvailable`）。也就是说**没选后端就没有任何搜索 UI 可用**，而不是静默返回空结果——这个“诚实不可用”值得抄。
- 四个后端由 `internal/search/import.go` 的空白 import 注册进 `NewMap`，新增后端只需实现接口 + 注册。

### 1.3 调用流程（一次搜索）

```
POST /api/fs/search  {parent, keywords, scope, page, per_page, password}
  → middlewares.SearchIndex（模式 != none 才放行）
  → handles.Search（server/handles/search.go）
      → user.JoinPath(parent)：把请求路径约束进当前用户的 BasePath
      → req.Validate()：page / per_page 必须 ≥ 1
      → search.SearchFiltered(ctx, req, isSearchNodeAccessible)
          → 后端若实现 FilteredSearcher（只有 bleve）：走后端原生 SearchFiltered
          → 否则：以后端 Search 分页取（每批 1000 条），内存里过权限过滤器，
            再按请求页切片，total = 过滤后的总数
      → 每条结果补 Type 字段（utils.GetObjType：按扩展名分图片/视频/音频/文档等）
  → PageResp{Content, Total}
```

关键点：**权限过滤发生在分页之前**（见 `internal/search/search_test.go:TestSearchFilteredFiltersBeforePagination`，该测试专门锁定这一行为）。`isSearchNodeAccessible` 逐条检查：节点父目录是否在用户 BasePath 内 + 最近的 Meta（加密/密码目录）是否允许该用户访问（`common.CanAccess`，带请求里传的 password）。代价是：通用回退路径下，一次查询可能要把后端结果全扫一遍才拼出 requested page。

### 1.4 调用流程（一次建索引）

```
POST /api/admin/index/build（管理员）
  → handles.BuildIndex：若 Running() 则 400；起 goroutine
      → search.Clear()：先清空旧索引
      → search.BuildIndex(ctx, ["/"], ignorePaths, maxDepth=20, count=true)
          → 单例守卫（atomic CAS，见 §3）
          → fs.Get("/") + fs.WalkFS(admin 上下文，maxDepth) 串行遍历
          → 每节点进内存 MQ，消费协程每 1 秒（或队列 ≥1000、或每 5 拍）批量写后端
          → 进度写 SettingItem（IndexProgress JSON）
```

---

## 2. 数据模型：索引里存了什么

### 2.1 `SearchNode`——只有 4 个字段

`internal/model/search.go:23`：

```go
type SearchNode struct {
    Parent string `json:"parent" gorm:"index"`
    Name   string `json:"name"`
    IsDir  bool   `json:"is_dir"`
    Size   int64  `json:"size"`
}
```

没有 mtime、没有 hash、没有 MIME/扩展名、没有内容、没有标签。**索引的全部信息就是“父目录 + 名字 + 是不是目录 + 大小”**。这直接决定了：Alist 的搜索只能按名字找，不能按时间、大小、类型、内容过滤（类型过滤是返回后由 `Type` 字段在展示层做的，不是索引字段）。

表 `search_nodes` 由 `internal/db/db.go:28` 的 `AutoMigrate` 创建。`Parent` 上有一个 B-tree 索引；**没有唯一约束**——同一个文件索引两次就会产生重复行（去重完全依赖上层流程：全量重建先 Clear，增量更新先做集合差分，见 §3、§4）。

### 2.2 `SearchReq`——查询请求

`internal/model/search.go:15`：

```go
type SearchReq struct {
    Parent   string `json:"parent"`
    Keywords string `json:"keywords"`
    // 0 for all, 1 for dir, 2 for file
    Scope    int    `json:"scope"`
    PageReq         // Page, PerPage
}
```

- `Parent` 是**子树根**，不是“只查直接子级”：db 后端用 `parent = X OR parent LIKE 'X/%'`（`internal/db/searchnode.go:whereInParent`），`/` 表示全库。
- `Keywords` 是原始字符串，各后端自己拆词（db 后端按空白切词后 AND；MySQL 整串拼进布尔模式；见 §6）。
- `Scope`：0 全部、1 只目录、2 只文件。

### 2.3 `IndexProgress`——进度存在设置表里

`internal/model/search.go:8`：`{ObjCount, IsDone, LastDoneTime, Error}`。`internal/search/util.go:Progress()/WriteProgress()` 把它以 JSON 形式存为一条 `SettingItem`（key=`index_progress`，`internal/conf/const.go`）。**进度和索引数据不在同一张表**，且写进度走的是同一套设置保存路径（同一个 SQLite 库）——与锁库问题有关（见 §7）。

### 2.4 `Searcher` 接口

`internal/search/searcher/searcher.go`：

```go
type Config struct {
    Name       string
    AutoUpdate bool   // 是否支持增量更新
}

type Searcher interface {
    Config() Config
    Search(ctx, req) ([]SearchNode, int64, error)
    Index(ctx, node) error
    BatchIndex(ctx, nodes []SearchNode) error
    Get(ctx, parent string) ([]SearchNode, error)  // 取某父目录下的已索引节点（供增量差分）
    Del(ctx, prefix string) error                  // 按路径前缀删（含子孙）
    Release(ctx) error
    Clear(ctx) error
}

type FilteredSearcher interface {
    SearchFiltered(ctx, req, filter Filter) ([]SearchNode, int64, error)
}
```

注意 `Get` 和 `Del` 是增量更新的必需品：不支持它们的后端（bleve）就做不了增量更新（见 §4.4）。

---

## 3. 建索引：全量流程、进度、开销

入口：`internal/search/build.go:BuildIndex(ctx, indexPaths, ignorePaths []string, maxDepth int, count bool)`。

### 3.1 单例守卫

```go
quit := make(chan struct{}, 1)
if !Quit.CompareAndSwap(nil, &quit) {
    return errs.BuildIndexIsRunning   // "build index is running, please try later"
}
```

`Quit` 是包级 `atomic.Pointer[chan struct{}]`。同一时间只能有一个建索引任务；`Running()` 就是 `Quit.Load() != nil`。停止（`handles.StopIndex`）是往 quit channel 发一个信号，消费协程收到后做最后一次落盘并写 `IsDone=true` 的进度后退出。

### 3.2 遍历：串行 Walk

- 对每个 `indexPath`：先 `fs.Get` 取根节点对象，再 `fs.WalkFS(admin 上下文, maxDepth, …)`（`internal/fs/walk.go`）。
- `WalkFS` 的深度语义：`depth == 0` 时不再下钻；每深入一层 `depth-1`。默认 `maxDepth=20`（`internal/bootstrap/data/setting.go:205`，`max_index_depth` 默认 `"20"`；`handles.BuildIndex` 用 `setting.GetInt(conf.MaxIndexDepth, 20)`）。**超过 20 层的文件永远不会被索引，也就永远搜不到，且没有任何提示。**
- 遍历是串行的，代码里留有 `// TODO: run walkFS concurrently`。
- `walkFn` 里三层跳过：
  1. `indexPath == "/"`（根节点本身不索引）；
  2. `ignorePaths` 前缀匹配（`strings.HasPrefix(indexPath, avoidPath)`）；
  3. 该路径所属存储的 `DisableIndex` 为真（`internal/model/storage.go:20`，挂载时可勾选）。
- 每访问一个节点就往内存消息队列发一条 `ObjWithParent{Obj, Parent: path.Dir(indexPath)}`。

一个细节：`WalkFS` 内部调 `fs.List`，而 `fs.List` 最终调 `op.List`，`op.List` 每次列目录都会触发 `ObjsUpdateHook`（除非 SkipHook）。建索引期间增量更新函数 `Update()` 因 `Running()` 守卫直接返回，所以不会递归触发增量更新，只产生一批空转的 hook goroutine（`internal/op/fs.go:80` 是 `go HandleObjsUpdateHook(...)` 异步调用）。

### 3.3 写入：内存队列 + 定时批量落盘

- 队列是 `pkg/mq` 的 `mq.NewInMemoryMQ[ObjWithParent]()`（纯内存，无持久化；进程崩溃则队列里未落盘的条目丢失）。
- 消费协程 1 秒一个 tick：
  - 队列长度 `< 1000` 且 tick 计数未到 5 → 跳过；
  - 队列 `>= 1000` 或每 5 拍（5 秒）→ `ConsumeAll`，调 `BatchIndex` 批量写后端。
- 各后端批量大小不同：db 后端 `CreateInBatches(…, 1000)`（`internal/db/searchnode.go`）；bleve 一个 batch 全写；meilisearch `AddDocumentsInBatches(…, 10000)`。
- `count=true` 时每次落盘后写进度（`WriteProgress`）。建索引入口 `handles.BuildIndex` 传 `count=true`；`handles.UpdateIndex`（按路径重建子树）传 `count=false`，完成后自己写一次进度——注意它写回的是**重建前读到的旧 `ObjCount`**，不是重建后的实际数，展示的总数可能偏小。

### 3.4 全量重建的语义

`handles.BuildIndex` 的 goroutine 里是**先 `Clear()` 再 `BuildIndex(["/"])`**：重建期间索引是空的，搜索返回空结果；中途崩溃则索引残缺。启动时 `internal/bootstrap/index.go:InitIndex()` 会把未完成的进度强行标为 `IsDone=true`——**崩溃后残缺的索引会被呈现为“已完成”**，这是个坑（见 §7）。

### 3.5 开销

- 首次建索引的成本不在索引本身，而在遍历：每个目录一次 List 调用（走各家网盘 API），9 个挂载全量爬一遍就是全部的列表配额 + 时间。索引表本身很小（每行 4 个字段）。
- `search_nodes` 表会随对象数膨胀；清空后需要 `VACUUM` 才能把 SQLite 文件瘦下来（官方文档提及）。

---

## 4. 增量更新：hook 机制、触发点、bleve 为什么只能全量重建

### 4.1 注册与守卫

`internal/search/build.go` 末尾：

```go
func init() {
    op.RegisterObjsUpdateHook(Update)
}
```

`Update(ctx, parent string, objs []model.Obj)` 的守卫链（全部满足才执行）：

1. `instance != nil`（已选后端）；
2. `instance.Config().AutoUpdate` 为真；
3. 设置项 `auto_update_index` 为真——**默认值是 `false`**（`internal/bootstrap/data/setting.go:203`），即增量更新默认关闭；
4. 不在全量建索引中（`Running()`）；
5. `parent` 不在忽略路径里；
6. 已有一次完成的建索引（`progress.IsDone`）。

### 4.2 差分算法（db / db_non_full_text 后端）

```go
nodes, _ := instance.Get(ctx, parent)   // 索引里该父目录下的旧集合
now := {objs 里的名字集合}                // 当前文件系统状态
old := {nodes 里的名字集合}
toDelete := old - now
toAdd    := now - old
// 删：逐个 Del(path.Join(parent, name))，但跳过本身就是一个挂载点的路径
//     （!op.HasStorage(path)，避免误删挂载子树）
// 加：收集后一次 BatchIndex
```

只按**名字**做集合差分，不比较大小/mtime：同名文件内容变了不会触发重新索引（但索引里本来就没有内容/mtime，所以“重新索引”也没有意义——名字没变索引行就没变）。**改名 = 删旧名 + 加新名**，天然处理。

### 4.3 并发：per-parent 锁

`internal/search/update_lock.go`：`map[string]*updateLock` + 引用计数，`lockUpdate(parent)` 让**同一父目录的更新串行**，不同父目录可并发。锁对象引用归零后从 map 里删除，避免泄漏。

### 4.4 触发点：哪些操作会走到 Update

hook 的生产者是 `internal/op`（`internal/op/hook.go:RegisterObjsUpdateHook / HandleObjsUpdateHook`），调用点：

| 调用点 | 触发条件 | 说明 |
|---|---|---|
| `internal/op/fs.go:List`（约 80 行） | 每次列目录成功后，`!args.SkipHook`，**异步 goroutine**（`context.WithoutCancel`） | 浏览即更新：用户打开一个文件夹，该目录的索引就被刷新。这是增量更新最主要的触发源 |
| `Move` / `Rename` / `Copy` / `Put` / `PutURL`（约 451/511/577/709/776 行） | 写操作完成后调 `objsUpdateHook`（递归或非递归），但受 `needHandleObjsUpdateHook()` 门控 | 门控 = 设置项 `handle_hook_after_writing` 为真；否则写操作不触发 |
| `internal/op/archive.go:537` | 归档相关（触发条件未细读） | 未确认 |
| `objsUpdateHook` 的递归版 `recursivelyObjsUpdateHook` | 递归列子目录，可配限速（`handle_hook_rate_limit`） | 目录树整体变动时逐层刷新 |

注意不对称：**读（List）无条件触发，写（Move/Rename/Copy/Put）还要再过一个设置项开关**。List 是高频操作，意味着增量更新的实际触发频率 ≈ 用户浏览频率。

### 4.5 meilisearch：异步任务队列

`Update()` 里对 meilisearch 有特判：不直接做差分，而是 `EnqueueUpdate(parent, objs)` 进任务队列（`internal/search/meilisearch/task_queue.go`）：

- 按 parent 去重（同一 parent 的新快照覆盖旧任务）；
- 30 秒一个 tick 消费；浅层路径先处理，再按入队时间；
- 消费时才查索引现状做差分（`executeTask` 内 `Get(parent)` 再算 now/old 差集），然后先删后加，记录返回的 taskUID；
- 下一轮消费前先查之前任务的状态，`enqueued`/`processing` 的任务会让该 parent 的任务重新入队等待。

这样做是因为 Meilisearch 的写操作是异步任务（提交只表示“已入队”），直接差分会读到旧状态而算错。

### 4.6 bleve 为什么只能全量重建

三点合起来决定的：

1. `bleve.Config` 只设了 `Name: "bleve"`，**`AutoUpdate` 是零值 `false`**（`internal/search/bleve/init.go`），`Update()` 在第二道守卫就直接返回；
2. `Bleve.Get()` / `Bleve.Del()` 直接返回 `errs.NotSupport`（`internal/search/bleve/search.go`）——没有“查某目录旧节点”和“按前缀删”的能力，差分算法根本跑不起来；
3. `Bleve.Clear()` 是关闭索引 → `os.RemoveAll(BleveDir)` → 重建空索引，即**清空 = 删整个索引目录**。

所以 bleve 用户想更新索引只有一条路：管理页“重建索引”（先 Clear 再全量 Build）。`handles.UpdateIndex` 也会先检查 `Config().AutoUpdate`，bleve 下直接返回 400 `update is not supported for current index`。

---

## 5. 查询路径：API 入口 → 后端 → 结果返回

### 5.1 路由

`server/router.go:298`：`g.Any("/search", middlewares.SearchIndex, handles.Search)`；索引管理在 `233–237` 行：`POST /api/admin/index/build|update|stop|clear`、`GET /api/admin/index/progress`（均过 `SearchIndex` 中间件）。

### 5.2 db 后端查询（`internal/db/searchnode.go:SearchNode`）

```go
// SQLite 或非全文模式：每关键词一个 name LIKE %kw%，AND 连接
keywordsClause := db.Where("1 = 1")
for _, keyword := range strings.Fields(req.Keywords) {
    keywordsClause = keywordsClause.Where("name LIKE ?", "%"+keyword+"%")
}
searchDB = db.Model(&SearchNode{}).Where(whereInParent(req.Parent)).Where(keywordsClause)

// MySQL 全文：MATCH (name) AGAINST ('*keywords*' IN BOOLEAN MODE)
// Postgres 全文：to_tsvector(name) @@ to_tsquery('kw1 & kw2')
```

- **默认 SQLite 部署下，`useFullText` 参数被忽略**：`if !useFullText || conf.Conf.Database.Type == "sqlite3"` 直接走 LIKE 分支。所以名字叫 `database`（全文）的后端，在最常见的 SQLite 单机部署上实际是**子串 LIKE 查询**，不是全文索引。
- `whereInParent`：`parent = X OR parent LIKE 'X/%'`，即子树搜索；`parent == "/"` 时条件为 `1 = 1`。
- 排序固定 `ORDER BY name asc`（没有按相关度、时间、大小排序的选项）；分页是先 `Count` 再 `Offset/Limit`。
- 未确认：Scope 过滤那行写的是 `searchDB.Where(db.Where("is_dir = ?", isDir))`，**返回值被丢弃**。GORM v1.31.2（`go.mod`）的链式调用是否原地生效，本地没有 module cache 可查源码，**未确认**——若失效，Scope=1/2（只搜目录/只搜文件）会静默返回全部类型，需实测。

### 5.3 bleve 后端查询

`buildQuery`：`MatchQuery(keywords)` 限定 `name` 字段 + 可选的 `is_dir` 布尔查询，AND 组合；排序 `name, _id`；`From/Size` 分页；`Fields: ["*"]` 全字段返回。

注意：**查询本身不含 parent 条件**——文件夹范围靠 `SearchFiltered` 原生实现里逐条 `utils.IsSubPath(req.Parent, node.Parent)` 过滤。直接调 `search.Search`（非 Filtered）会跨目录返回结果；但仓库内唯一调用方是 handler，handler 固定走 `SearchFiltered`，所以实际无影响。另：`search.Search` 与单条 `search.Index` 在仓库内**没有任何调用方**，属于闲置导出函数。

### 5.4 meilisearch 后端查询

过滤条件拼成 `parent_path_hashes = '<hash>'`（子孙）+ `is_dir = true/false`，`AttributesToSearchOn` 只含 `name`。`Page` / `HitsPerPage` 直接透传 Meilisearch 的分页。排序是 Meilisearch 默认的相关度排序。

### 5.5 分页与排序小结

| 后端 | 排序 | 分页方式 | 范围过滤位置 |
|---|---|---|---|
| database / LIKE | name asc | Count + Offset/Limit | SQL WHERE（子树 LIKE） |
| bleve | name, _id | From/Size；Filtered 用 searchAfter 游标 | SearchFiltered 内内存过滤 |
| meilisearch | 相关度（默认） | Page / HitsPerPage | Meilisearch Filter |
| 通用回退（无 FilteredSearcher 时） | 取决于后端 | 后端分页每批 1000，内存过滤后再切片 | 内存 |

---

## 6. 四个后端对比

| 后端 | 实现方式 | 优点 | 缺点 | 适用场景 |
|---|---|---|---|---|
| **database**（全文） | 共用 `search_nodes` 表；SQLite 下走 `LIKE %kw%`；MySQL 建 `FULLTEXT` 索引走 `MATCH…AGAINST` 布尔模式；Postgres 建 GIN 索引走 `to_tsvector @@ to_tsquery` | 零额外部署（复用主库）；支持增量更新；MySQL/Postgres 下是真全文 | SQLite 下名不副实（实为 LIKE）；MySQL/Postgres 的全文分词对中文不友好（见 §7）；大库 LIKE 扫表慢 | 默认选择；SQLite 单机部署 |
| **database_non_full_text** | 与 database 同表同写，只在查询时强制走 LIKE 分支 | 行为可预测（永远是子串匹配）；中文子串能搜到 | 大库性能最差；与 database 后端在 SQLite 下完全等价（纯属命名区分） | 明确只要子串匹配、或 MySQL/Postgres 下不想建全文索引时 |
| **bleve** | 内嵌 bleve v2 索引（目录 `BleveDir`）；name 字段 Keyword 映射、parent 文本映射 | 无需外部服务；真倒排索引，查询快 | **不支持增量更新**（Get/Del 返回 NotSupport，AutoUpdate=false），只能全量重建；name 用 Keyword 映射（整串一个 token，无中文分词，代码里留着 `TODO: appoint analyzer`）；文档 ID 是随机 UUID，重建不幂等（不清库会产生重复文档）；Clear 是删整个目录 | 数据量小、变动少、能接受定期全量重建时 |
| **meilisearch** | 外部 Meilisearch 服务；文档 ID = 路径 SHA-1，另存父哈希与祖先哈希链；可过滤属性 `parent/is_dir/name/parent_hash/parent_path_hashes`，可搜索属性仅 `name` | 真全文 + 相关度排序；增量更新有专用异步任务队列（去重、浅层优先、等前序任务完成）；删除用祖先哈希过滤，一次请求删整个子树 | 需要额外部署服务（host + API key 配在 conf 里）；写操作异步，刚上传的文件不能立即搜到；`Get` 用 `Limit: MaxInt` 全量拉取，大目录可能很重 | 已有 Meilisearch 基础设施、或追求搜索质量时 |

共同点：四个后端索引的**字段完全相同**（parent/name/is_dir/size），切换后端不需要改数据模型，只需要重建索引。

---

## 7. 坑与失败模式

### 7.1 SQLite `database is locked`

官方文档（alist.iots.vip 搜索页）记载：建索引期间、或开启自动更新时会出现 `database is locked`。代码里已有针对性缓解（`internal/bootstrap/sqlite_driver_glebarez.go` / `sqlite_driver_gorm.go`，两套构建）：

```
_journal=WAL & _busy_timeout=10000 & _synchronous=NORMAL & _txlock=immediate
（glebarez 版用 _pragma= 形式；注释说明 mattn 风格的参数会被静默忽略，
 曾导致回滚日志模式 + 驱动默认 5 秒 busy timeout）
```

WAL 允许多读单写，10 秒 busy timeout 让写操作排队等待，`_txlock=immediate` 让写事务一开始就拿 RESERVED 锁，避免 SHARED→RESERVED 升级死锁。但缓解不等于根治：锁的根源是**同一 SQLite 文件上的多路写并发**——建索引的批量写事务（`CreateInBatches` 每批 1000 行）与写进度（每次落盘都 `WriteProgress` 写设置表）、与增量更新的读写、与主业务的写同时发生。以下触发时序为推断，**未确认**：长批量写事务持有写锁期间，另一路写操作等满 10 秒即报 `database is locked`。另：`auto_update_index` 默认 `false` 与此有关——默认关闭增量更新能显著降低锁库概率。

### 7.2 中文分词：只有 LIKE 是真子串

- **SQLite LIKE**：`%报告%` 是字节级子串，中文能搜到——是四个后端里唯一对中文子串友好的。
- **MySQL FULLTEXT**：建索引时未指定 parser（`internal/search/db/init.go` 只写 `CREATE FULLTEXT INDEX`），默认 parser 按空白/标点分词，中文整串被当成一个 token；`MATCH…AGAINST('*keywords*' IN BOOLEAN MODE)` 是按词匹配，不是子串。查“报告”命中不了“年度报告”。（布尔模式前导 `*` 的具体语义未逐字核对 MySQL 文档，**未确认**。）
- **Postgres**：`to_tsquery('kw1 & kw2')` 同样是词级别匹配；CJK 在默认 parser 下同样不成词。
- **bleve**：name 字段是 Keyword 映射（整串一个 token），且代码留着 `TODO: appoint analyzer`——没有中文分词器，也没有 n-gram；官方文档称 bleve“结果奇怪、资源占用大”。
- 结论：**Alist 的“全文”在中文场景下基本等于“按词匹配”，只有 LIKE 是真子串**。这是抄它时最容易踩的坑。

### 7.3 深度上限静默截断

`max_index_depth` 默认 20，`WalkFS` 到深度就停，超深文件不进索引、搜不到、**无任何提示**。用户只会觉得“搜索不全”，不会知道是深度原因。

### 7.4 忽略路径是前缀匹配

`isIgnorePath` 与建索引的 `walkFn` 都用 `strings.HasPrefix(path, ignorePath)`。忽略 `/data` 会连带忽略 `/data2`、`/database`。应按路径段边界匹配，Alist 没做。

### 7.5 循环挂载保护

`internal/search/util.go:updateIgnorePaths`：

- `Virtual` 驱动的挂载永远不索引；
- `OpenList`（挂另一个 Alist/OpenList 实例）的挂载默认不索引，除非远程站点的 `/api/public/settings` 返回 `allow_indexed == "true"`（每个地址只查一次，结果缓存）；
- 再叠加用户在设置里填的 `ignore_paths`（每行一个）与每个存储的 `DisableIndex` 开关。

这是为了防止 A 索引 B、B 索引 A 的循环，以及索引远程实例时产生指数级放大。但注意：这套保护只在**建索引前刷新忽略列表**时算一次（设置项 hook 与存储 hook），中途改了远程站点的 `allow_indexed` 不会实时生效。

### 7.6 其他

- **崩溃后进度撒谎**：`bootstrap.InitIndex()` 把未完成的进度标为完成（§3.4），残缺索引看起来是完整的。
- **重建先清空**：`BuildIndex` 处理器先 `Clear()` 再建，重建期间搜索为空；`UpdateIndex` 只删子树再重建，但写回的是旧 `ObjCount`，总数展示可能偏小。
- **无唯一约束**：`search_nodes` 无唯一键；任何绕过“先清后建 / 先删后建”的重复写入都会产生重复行，并在搜索结果里重复出现。
- **meilisearch 的异步滞后**：`BatchIndex` 提交后不等任务完成就返回；刚传完的文件要等 Meilisearch 索引完才能搜到，滞后时间不确定。
- **搜索默认关闭**：`search_index` 默认 `none`；用户必须手动选后端并建索引，否则搜索入口直接 404。好处是诚实，代价是新用户第一眼以为“没有搜索功能”。
- **权限过滤的性能**：通用回退路径下，每次查询都要把后端结果分批拉全再过滤；受限用户（能看的目录少）在大库上搜一次可能很慢。

---

## 8. 给 ZyDrive 的建议

背景：ZyDrive 是 Go + mygo 桌面多网盘管理器，已有 SQLite 缓存与可选的 Provider Search 接口；同步引擎是 Scan → Compare → Plan → Confirm → Execute；客户端加密的本地索引设计见 `docs/client-encryption.md` 第八节。

### 8.1 直接抄的

1. **Searcher 接口 + 注册表**：`Searcher{Config/Search/Index/BatchIndex/Get/Del/Release/Clear}` + `FilteredSearcher` + `NewMap` 注册，与 ZyDrive 现有的 Provider 可选接口（Search/Delta/Thumbnail…）是同一形状。后端可插拔，`database` 先行，bleve/meilisearch 以后再加，不用改调用方。
2. **四字段索引模型**：`parent / name / is_dir / size` 足够做 v1 的文件名搜索。不要一开始就加 mtime/hash/内容字段——字段越多，增量更新的差分逻辑越复杂。
3. **子树语义**：`Parent` 参数表示子树根（`parent = X OR parent LIKE 'X/%'`），`/` 表示全库；文件夹范围搜索天然就有，不用另做。
4. **差分式增量更新**：`Get(parent)` 取旧集合 → 与当前列表按名字做集合差 → 删旧加新。比“删了整目录重建”便宜一个数量级；改名天然等于删旧加新。
5. **per-parent 锁**：同一目录串行、不同目录并发（`update_lock.go` 的引用计数锁），直接复用思路。
6. **先过滤后分页**：权限过滤必须发生在分页之前，否则总数和页内容都会错；用测试把这条锁死（Alist 有 `TestSearchFilteredFiltersBeforePagination`，照抄一个）。
7. **索引状态可见**：没建好就明说“搜索不可用”，不要静默返回不全的结果；建索引给进度（已索引数 / 是否完成 / 上次完成时间）。
8. **meilisearch 的路径哈希技巧**：文档 ID = 路径 SHA-1（大小写敏感的精确匹配），另存祖先路径哈希链做子树过滤。这招不依赖 Meilisearch——ZyDrive 的 SQLite 索引也可以用“路径哈希做主键 + 祖先哈希链做子树查询”代替 `LIKE 'X/%'` 前缀匹配，顺带解决前缀碰撞（§7.4）和 LIKE 注入式误匹配。

### 8.2 要避的

1. **不要把 LIKE 后端叫“全文”**：Alist 的 `database` 后端在 SQLite 下实际是 LIKE，命名与行为不一致。ZyDrive 的 v1 就老实叫“文件名搜索”，内容搜索以后由各家服务端搜索接口补。
2. **不要抄 bleve 的 Keyword 映射**：中文子串搜不到；要上 bleve/全文引擎必须先配好中文分词或 n-gram，否则不如 LIKE。
3. **不要默认开增量更新**：Alist 把 `auto_update_index` 默认设 `false` 是对的——增量更新的写放大是锁库的来源之一。先让用户手动建索引、手动刷新，跑稳了再考虑默认开。
4. **不要把进度存在同一个 SQLite 库还每批写一次**：`WriteProgress` 每次落盘都写设置表，是锁库的帮凶之一。进度要么放内存 + 结束时写一次，要么节流（如每 5 秒写一次）。
5. **崩溃后不要把未完成标成完成**：启动时残缺索引应标为“未完成/需重建”，而不是 `IsDone=true`。
6. **不要先 Clear 再慢慢建**：重建期间搜索为空。应建新索引再原子切换，或至少在 UI 上标明“重建中，结果不完整”。
7. **文档 ID 不要用随机 UUID**：bleve 每条文档随机 UUID，重建不幂等。用路径哈希做 ID（meilisearch 的做法），重建同一文件就是同一文档，天然去重。
8. **忽略路径不要用 `HasPrefix`**：`/data` 会误伤 `/data2`。按路径段边界匹配，或存哈希后精确比对。

### 8.3 ZyDrive 可以做得比 Alist 好的地方

1. **增量更新复用同步引擎**：Alist 的增量靠“浏览时 List 触发 hook”——用户不浏览的目录就不同步。ZyDrive 的同步本来就要 Scan 全量比对，**同步扫描时顺手更新搜索索引，零额外遍历**，比 List hook 更彻底，还能覆盖用户从不打开的目录。
2. **索引与加密索引合一**：`docs/client-encryption.md` 第八节已经设计了“本地解密后索引”（SQLite，master.key 加密存放）。搜索索引就是这套索引的“名字”部分——同一张表、同一份增量更新逻辑，不要为搜索再建第二套索引、第二套更新通道。加密文件的搜索直接走解密后的名字，零下载。
3. **服务端搜索做内容层**：Drive（`fullText`）、Dropbox（`search_v2`）、OneDrive（Graph）、Box 都有服务端内容搜索；本地索引只做文件名。两层合并展示（本地即时出结果，服务端结果随后补上并标注“实时”），就是之前定的混合方案——Alist 没有这一层（它连 Provider 搜索接口都没有，全靠本地索引）。
4. **Agent 端不建索引**：索引是桌面本地的；Agent 只跑同步任务，不需要搜索索引，避免在服务器上再维护一份。
5. **排除规则复用同步的排除规则**：同步任务已有的默认排除（`.DS_Store`、`Thumbs.db`、`.git/` 等）直接复用到索引，少一套配置。

### 8.4 落地顺序建议

1. v1：`database`（LIKE）后端 + 四字段表 + 全量建索引（后台任务、可暂停、进度可见）+ 文件夹范围搜索 + 先过滤后分页。只搜文件名，明确写在 UI 上。
2. v1.1：增量更新——先接同步引擎的 Scan 结果（顺手更新），再接 List hook（浏览时刷新）；默认关闭自动更新，手动刷新先行。
3. v1.2：Provider Search 接口接通 Drive/Dropbox/OneDrive/Box 的服务端搜索，做内容补全与合并展示（标注实时/缓存）。
4. 以后：bleve 或 SQLite FTS5 二选一做本地全文（内容索引），但要先解决中文分词（FTS5 的 unicode61 不分中文，需自定义分词器或 trigram）；meilisearch 不建议作为桌面产品的默认后端（需额外部署服务）。

---

## 附：关键文件索引

| 内容 | 路径 |
|---|---|
| 门面（单例、Init、SearchFiltered） | `internal/search/search.go` |
| 全量建索引 + 增量 Update | `internal/search/build.go` |
| Searcher 接口 | `internal/search/searcher/searcher.go` |
| 后端注册表 | `internal/search/searcher/manage.go` |
| 后端空白 import | `internal/search/import.go` |
| db 后端（查询 SQL 核心） | `internal/db/searchnode.go` |
| db 后端全文索引创建 | `internal/search/db/init.go` |
| bleve 后端 | `internal/search/bleve/search.go`、`internal/search/bleve/init.go` |
| meilisearch 后端 + 任务队列 | `internal/search/meilisearch/search.go`、`internal/search/meilisearch/task_queue.go`、`internal/search/meilisearch/utils.go` |
| 数据模型 | `internal/model/search.go` |
| 搜索 API 入口 | `server/handles/search.go` |
| 索引管理 API | `server/handles/index.go` |
| 搜索中间件 | `server/middlewares/search.go` |
| 路由 | `server/router.go`（搜索 298 行，索引管理 233–237 行） |
| hook 注册与触发 | `internal/op/hook.go`、`internal/op/fs.go`（List/Move/Rename/Copy/Put/PutURL） |
| per-parent 锁 | `internal/search/update_lock.go` |
| 进度/忽略路径工具 | `internal/search/util.go` |
| 默认设置 | `internal/bootstrap/data/setting.go:202–205` |
| 启动时进度处理 | `internal/bootstrap/index.go` |
| SQLite 驱动参数（WAL/锁） | `internal/bootstrap/sqlite_driver_glebarez.go`、`internal/bootstrap/sqlite_driver_gorm.go` |
| 遍历实现 | `internal/fs/walk.go` |
| 权限过滤测试 | `internal/search/search_test.go` |
