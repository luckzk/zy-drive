# ZyDrive（多网盘管家）

桌面版 ZyDrive，走 Air Explorer 的功能路线，面向新手。

一个窗口管理 Dropbox、Google Drive、OneDrive、S3 / R2 与 Drime：浏览、
上传下载、单向复制与双向同步。桌面管配置和看，服务器 Agent 管定时执行。
**不用 rclone，全部走各家官方 API 原生实现。**

> 状态：V0.1 功能填充（2026-11-24 晚）。S3、Dropbox、Google Drive
> 驱动均已真实现（API v2 / v3 HTTP，无第三方 SDK），本地文件夹
> 驱动已实现；OneDrive / Drime 待 V0.2。mygo 窗口/菜单/托盘/
> Bind 已接线（`cmd/desktop/mygo.go`，无 webview 时回落 dev 壳）。
> 架构设计见 `docs/architecture.md`（仓库版）与
> `~/workspace/your_files/multi-cloud-manager-architecture/multi-cloud-manager-architecture.pdf`（18 页）。

## 已定决策（不再重议，要改先改架构文档）

| 决策 | 内容 |
|---|---|
| 形态 | 桌面先行，不做 Docker 网页版；同步任务可 offload 到服务器 Agent |
| 引擎 | 不用 rclone；五家云全部走官方 API / SDK 原生实现 |
| 技术 | Go 1.27 纯 Go（无 cgo）+ mygo Web 前端模式（React + Tailwind）+ SQLite（纯 Go 驱动） |
| 界面 | 后端逻辑借鉴 Pail，UI 层重写（Pail 只支持单个 S3，产品是多网盘）；专业项留着，默认收进高级/详情里，按能力表有无显示 |
| 同步安全 | 删除必先预览再确认；双向冲突绝不静默删（新版占名，旧版留“冲突副本”） |
| 密钥 | refresh_token、Secret Key 只存密文，日志不落明文 |

## V0.1 范围

- S3 + Dropbox 两家；浏览、上传下载、单向复制（Copy 先做，Mirror 带预览）
- 同步任务表（方向、源/目标、上次成功时间），跑之前出计划预览
- 双向同步简化版：规则定死，新版占名，另一版留冲突副本
- 同步任务仅本机执行；Agent（5 个 API + 调度器骨架）先搭好，V0.2 再联调
- 不做：Facebook / Instagram（后期只读导入）、Docker 网页版、40+ 云全量

## 快速开始

```sh
# 需要 Go 1.27+。本机工具链在 ~/tools/go/bin/go（go.dev 官方包）
export PATH=$HOME/tools/go/bin:$PATH

make tidy
make vet
make test
make build

# 桌面（mygo 窗口壳，失败时回落到本地 HTTP dev 壳）
./build/mcm-desktop --config-dir ~/.config/zy-drive

# Agent（无界面）
./build/mcm-agent --addr 127.0.0.1:8080 --config-dir ~/.config/zy-drive-agent

# CLI
./build/mcm ls s3://my-bucket/photos/
./build/mcm sync ./photos s3://my-bucket/photos/ --dry-run
```

> 二进制名暂沿用 `mcm-*`（`mcm-desktop` / `mcm-agent` / `mcm`），改名工作量大故未随产品名一起改；配置目录已随产品名改为 `~/.config/zy-drive`（Agent 为 `~/.config/zy-drive-agent`）。

## 目录

```
cmd/desktop/   桌面入口（只组装）：应用层 + 本地 HTTP 开发壳，mygo 窗口接线见下
cmd/agent/     Agent 入口（只组装）：API + 调度器，无界面
cmd/cli/       CLI 入口：ls / cp / sync --dry-run
internal/app/      应用层：账号、挂载、浏览、传输、同步任务、事件（唯一业务编排入口）
internal/domain/   核心模型，不依赖任何其他包
internal/storage/  Provider 接口、能力表、注册表
internal/storage/providers/  s3（已实现）/ dropbox（占位）/ _template（新云脚手架）
internal/sync/     同步引擎：扫描、对比、计划、执行、冲突、基线状态
internal/transfer/ 传输引擎：队列、并发、限速、断点续传
internal/cache/    SQLite：节点缓存、path-ref 映射、游标、搜索、迁移
internal/auth/     OAuth、Token 刷新、密钥加密存取
internal/agentapi/ 桌面 ↔ Agent 协议：类型、服务端、客户端（两端共用）
internal/platform/ 公共层：HTTP 重试、OAuth、分片、调度（无业务状态）
internal/version/  应用版本、引擎版本、协议版本
frontend/          React + Tailwind（mygo Web 模式）
deploy/agent/      Agent Dockerfile + compose
```

## 依赖规则（违反即返工）

1. 界面不直连驱动：前端与桌面壳只能调应用层（`internal/app`）。
2. 上层不认厂商：同步、传输、搜索代码里不出现厂商判断；差异收进驱动与能力表。
3. 删除必预览；状态必落盘；密钥必加密；版本必协商（桌面与 Agent 先比协议版本）。

## 接入一家新云（7 步）

1. 复制 `internal/storage/providers/_template/` 改名，只实现核心接口与适用的可选接口。
2. 写能力表：定位方式、删除语义、分片参数、路径规则、限流参数，全部显式声明。
3. 接公共层：HTTP 重试与退避、OAuth 刷新、分片上传会话，禁止在驱动里另写一套。
4. 写账号设置页：该云特有字段只出现在自己的设置页组件里。
5. 注册表加一行；Mount 创建向导自动出现，不改向导代码。
6. 跑 `internal/storage/contracttest` 一致性套件：分页、空目录、同名文件、续传、Token 刷新、429 退避。
7. 补增量策略：有游标接游标，没有就声明全量刷新间隔。

## mygo 接线说明

桌面 UI 已按定版 Demo（2026-11-24 晚，方案 A）搬进真实前端：分组侧栏、按需 Inspector、双栏同步任务、右下角传输胶囊、Agent 列表。数据全部走 `mygo.Bind`，无 mock。`cmd/desktop` 当前是 mygo
窗口 + dev 壳回落：`cmd/desktop/mygo.go` 负责窗口、菜单、托盘与
`mygo.Bind`，Bind 目标是 `internal/app/bind.go` 的 `DesktopService`
（专用类型，避免 `*App` 的非 JSON 方法让 Bind panic）。TS 客户端
由 `mygo generate` 生成到 `frontend/src/mygo.ts`（配置见 `mygo.json`，
需 `mygo-runtime`）。无系统 webview（`mygo.WebViewAvailable()` 为
false，如无 WebKitGTK 的 Linux VM）、前端未构建或 `App.Run` 失败时，
回落到 dev 壳（`--dev-shell` 强制）：本地 HTTP 托管 frontend/ 并
暴露 `/api/health`，不崩进程。

```sh
npm install --prefix frontend   # 含 mygo-runtime
npm run build --prefix frontend # 产出 frontend/dist，mygo 窗口用它
./build/mcm-desktop --dev-shell # 不开窗口，只起 dev 壳
```

## 命名

项目已定名 ZyDrive，仓库 github.com/luckzk/zy-drive。
