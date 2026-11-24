# 接新云操作清单

照着做，不漏项。验收标准：接入第六家云时，git 改动只出现在
新驱动目录、注册表与它的设置页里。

1. 复制 `internal/storage/providers/_template/` 改名，只实现核心接口
   与适用的可选接口（`storage.Provider` + Searcher/DeltaSource/
   Sharer/Versioner/Thumber）。
2. 写能力表：定位方式、删除语义、分片参数、路径规则、限流参数，
   全部显式声明（见 `internal/domain/caps.go`）。
3. 接公共层：`internal/storage/http`（重试与退避）、`internal/storage/oauth`
   （刷新）、`internal/storage/upload`（分片会话）。禁止在驱动里另写一套。
4. 写账号设置页：该云特有的字段只出现在自己的设置页组件里
   （`frontend/src/providers/`，一家一个文件）。
5. 注册表加一行（`cmd/desktop` 与 `cmd/agent` 的组装处）；Mount 创建向导
   自动出现，不改向导代码。
6. 跑 `internal/storage/contracttest` 一致性套件：分页、空目录、同名文件、
   续传、Token 刷新、429 退避。
7. 补增量策略：有游标接游标，没有就声明全量刷新间隔。

预留但不实现：WebDAV、SFTP、FTP、Box、MEGA 的接入路径与上面完全相同。
Facebook、Instagram 将来以只读驱动接入，写能力全关，界面自动只剩
浏览与导入。

## 已落地的驱动（2026-11-24 V0.1）

- Dropbox（`internal/storage/providers/dropbox/`）：API v2 HTTP，
  路径制；`files/list_folder` + cursor 分页，`upload_session`
  分片（4 MiB），`content_hash`；OAuth 刷新走
  `internal/storage/oauth.FormRefresher`（Dropbox token 端点）。
  契约测试：`dropbox_test.go` 的 mock HTTP 服务器。
- Google Drive（`internal/storage/providers/drive/`）：Drive API v3
  HTTP，ID 制；path→ID 映射在驱动内（内存缓存，Mkdir/Rename/
  Delete/Upload 时失效）；`files.list` 分页，multipart 小文件、
  resumable 大文件（8 MiB），Google Docs 类 `files.export`
  转 docx/xlsx/pptx/png，删除进回收站，`changes.list` 增量。
  契约测试：`drive_test.go` 的 mock HTTP 服务器。
- 本地文件夹（`internal/storage/providers/local/`）：`local://`
  端，`root` 配置 + 相对引用；只在本机跑，见 ADR-9。
- 共享层复用：HTTP 重试/429/401 刷新一次走
  `internal/storage/http`；Token 单次刷新走
  `internal/storage/oauth.TokenSource`；分片会话模型走
  `internal/storage/upload.Session`。驱动里禁止另写一套。
- 多账号：驱动实例按 Mount 建，不按类型单例；`New(cfg)` 每次
  返回独立实例（独立 TokenSource、HTTP client、ID 缓存）。
  注册表只注册工厂（`cmd/desktop`、`cmd/agent` 各一行）。
