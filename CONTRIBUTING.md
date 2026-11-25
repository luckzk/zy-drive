# Contributing（贡献指南）

欢迎提 Issue 与 PR。提 PR 之前请先读 `docs/architecture.md` 与 README 里的
“已定决策”和“依赖规则”，保持与现有架构一致。

## 协议条款（提交 PR 即表示同意）

提交 Pull Request，即表示你同意：

- 你对后端 Go 代码的贡献，按 **MPL-2.0** 开源（根目录 `LICENSE`，文件头
  `SPDX-License-Identifier: MPL-2.0`）；
- 你对 `frontend/` 界面代码的贡献，按 **MIT** 开源（`frontend/LICENSE`，
  文件头 `SPDX-License-Identifier: MIT`）；
- 你确认自己有权按上述协议提交这些代码（是你自己写的，或已获得原作者
  按上述协议开源的许可）。

写明这一条，是为了以后万一要调整协议，不用再一个个找贡献者要授权。

是否签署正式的 CLA（贡献者许可协议），以后再定；在此之前，以本条款为准。

## 免费 / Pro 分界线（代码放哪）

Pro 功能的实现代码一律放私有仓库
[`luckzk/zy-drive-pro`](https://github.com/luckzk/zy-drive-pro)，不进公开
仓库。公开仓库只留接口、免费版行为和 Pro 入口 UI（点进去显示升级提示
的那种）。

| 放公开仓库 `zy-drive` | 放私有仓库 `zy-drive-pro` |
|---|---|
| 接口定义（Pro 功能长什么样） | Pro 功能的真正实现 |
| 免费功能本身 | License 激活验证（离线激活码、设备绑定、Pro 开关） |
| Pro 入口 UI 与升级提示 | |
| 调用接口的接线代码、License 校验的调用点（不含校验实现） | |

Pro 功能清单（按 `docs/monetization.md` 第 6 节）：Mirror / 双向同步、
定时调度跑、并发与限速放开、批量传输 / 断点续传 / 冲突可视化、Agent
定时跑。

构建方式：Go build tag `pro`。公开版不带 pro 模块也能编译出来，就是免
费版；发 Pro 版时把 pro 模块编进去。两边版本要对上（core vX.Y.Z 对应
pro vX.Y.Z）。

已知例外：v0.9.0 的调度器（`internal/agentrun`）目前还在公开仓库里，是
先跑通的底座；上付费墙时把执行部分移到 pro 接口后面，任务模型与 UI 留
在公开仓库。

## 新文件记得加 SPDX 标识

- 后端 Go 文件首行：`// SPDX-License-Identifier: MPL-2.0`
- `frontend/` 下的 TS / TSX / JS 文件首行：`// SPDX-License-Identifier: MIT`
- 生成文件不用加（例如 `frontend/src/mygo.ts` 由 `mygo generate` 生成）；
  JSON、CSS、二进制与图片不用加。

## 开发流程

```sh
export PATH=$HOME/tools/go/bin:$PATH
make tidy
make vet
make test
make build
```

前端改动后，先跑 `scripts/visual-check/` 的截图自查，再对照用户确认过的
Demo v8 原型逐项比对，通过了再推。
