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
