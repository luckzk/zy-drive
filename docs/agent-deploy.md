# Agent 自部署指南（ZyDrive 0.9.0）

Agent 是 ZyDrive 的定时执行端：跑在你自己的服务器上，按时间表跑同步任务，
桌面关掉也能跑。**我们不提供服务器、不做账号/订阅、不做中央服务器**——
Agent 定时跑是 Pro 功能（激活码解锁，见 `docs/monetization.md`），
客户自己部署到自己的服务器，凭证由用户自己导出加密配置包、拷过去导入，
绝不经过我们的服务器。

## 部署方式（二选一）

### 方式 A：Docker（推荐）

```bash
cd deploy/agent
# 1. 先生成一个 API 令牌（桌面与 Agent 通讯用，改成你自己的随机串）
export ZY_DRIVE_AGENT_TOKEN="$(openssl rand -hex 24)"
docker compose up -d --build
docker logs -f zy-drive-agent
```

- 镜像 `zy-drive-agent:0.9.0`，只监听容器内 8080，compose 把它绑到
  `127.0.0.1:8080`——不要直接把 8080 发布到公网；需要远程访问时，
  在前面挂 Caddy/Nginx 反向代理（终止 TLS），或走 Tailscale。
- 数据在命名卷 `zy-drive-agent-data`（挂到 `/data`）：配置库、
  网盘凭证（master.key 加密）、任务与运行记录都在里面；删卷即丢。

### 方式 B：systemd（裸机 / VPS，不用 Docker）

```bash
# 1. 编译（或从发布页下载预编译二进制）
make build   # 得到 build/zy-drive-agent
sudo install -m 755 build/zy-drive-agent /usr/local/bin/zy-drive-agent

# 2. 令牌与服务
sudo install -d -m 700 /var/lib/zy-drive-agent
echo 'ZY_DRIVE_AGENT_TOKEN=<redacted>' | sudo tee /etc/zy-drive-agent.env
sudo chmod 600 /etc/zy-drive-agent.env
sudo cp deploy/agent/zy-drive-agent.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now zy-drive-agent
sudo systemctl status zy-drive-agent
```

单元文件见 `deploy/agent/zy-drive-agent.service`：以 `zy-drive-agent`
用户跑（需先建用户：`useradd -r -s /usr/sbin/nologin zy-drive-agent`），
数据目录 `/var/lib/zy-drive-agent`，只监听 `127.0.0.1:8080`。

## 配置下发：桌面导出 → 自己拷过去 → 服务器导入

凭证只走你自己的手，不经过任何第三方服务器：

1. **桌面端**：同步任务页 → 选 Agent → “导出配置包”，设一个口令
   （≥8 位）。导出的是一整个加密包（网盘配置 + 凭证 + 该 Agent 的任务），
   文件在下载目录，形如
   `zy-drive-agent-bundle-<agent>-20261125-143000.json`（0600）。
   - 用到本地文件夹的任务导不出去（服务器没有这台电脑的磁盘），
     导出时会明说跳过了哪几个，只留在桌面跑。
2. **自己拷过去**：`scp` / U 盘 / 你自己的同步盘，怎么方便怎么来。
3. **服务器上导入**（口令三种给法，任选其一）：
   ```bash
   zy-drive-agent import --file zy-drive-agent-bundle-ag-1-20261125-143000.json
   # 口令不给就交互输入（不回显）；或：
   ZY_DRIVE_BUNDLE_PASSPHRASE='<口令>' zy-drive-agent import --file <包>
   # 或 --passphrase <口令>（会留在 shell 历史里，不推荐）
   ```
   导入成功的输出会列出网盘与任务；定时任务立即接上调度器，
   Agent 重启后自动恢复（任务与运行记录存在配置库里）。

重新导出、再次导入会覆盖同名网盘与任务（按 ID），旧的运行记录保留。

## 日常使用

- **健康检查**：`curl http://127.0.0.1:8080/api/health`
  （返回 `{"online":true,"agentVersion":"0.9.0","protocolVersion":"1",...}`）。
- **桌面端**：设置 → Agent 里填 API 令牌；同步任务页能看到该 Agent 的
  任务、推送最新配置、“立即同步”、以及每次运行的记录
  （状态、计划、耗时、失败原因）。
- **定时**：任务的定时在桌面端设（手动 / 每小时 / 每天 HH:MM），
  推送或导入后生效；Agent 每分钟检查一次，到点就跑。
- **失败重试**：一次失败 10 分钟后自动重试一次（仅一次）；再失败就停，
  等你在桌面端看运行记录、修好原因后手动点“立即同步”。
- **定时任务绝不自动删文件**：计划里有删除操作的任务在 Agent 上
  直接失败并写明原因——删文件必须在桌面端看过删除清单、确认后手动跑。
  （Copy 方向的任务不受影响。）
- **换令牌**：改环境变量 / 服务的 EnvironmentFile 后重启 Agent，
  再在桌面设置 → Agent 里更新同一令牌。
- **升级**：停掉旧容器/服务，换新镜像或二进制，再启动；配置库在
  数据卷/目录里，升级不丢任务与运行记录。

## 排障

| 现象 | 原因 / 做法 |
| --- | --- |
| 桌面连不上 Agent（`测试`失败） | 先看 `curl /api/health` 通不通；再核对令牌与地址（默认只监听 127.0.0.1，需要远程访问时挂反向代理或改 `--addr`）；防火墙放行对应端口。 |
| 导入时报口令不对 | 口令与导出时不一致；配置包被改动过也会报同样的错（加密包自带完整性校验）。 |
| 任务推送成功、但跑起来报“请先导入配置包” | 推送只传任务配置，不传凭证——凭证只随配置包走。按上面流程导一次包。 |
| 运行记录里 `执行同步失败：…` | 按错误正文修：网盘凭证过期 → 桌面重新授权后重新导出导入；目标满了 → 清空间；网络问题 → 看 Agent 日志（`docker logs` / `journalctl -u zy-drive-agent`）。 |
| 定时到了没跑 | 任务 `Enabled` 是否打开；`NextRunAt` 是否在将来（桌面任务页能看到）；Agent 日志里找 `agentrun:` 行。 |
| 旧版 `MCM_AGENT_TOKEN` | 仍可用（兼容），但已改名 `ZY_DRIVE_AGENT_TOKEN`，建议改过来。 |
