# 多网盘授权方式对比（2026-11-26）

调查范围：Air Explorer、rclone、Alist / OpenList、Cyberduck / Mountain Duck
四家同类产品 + 标准 RFC 8252，对照 ZyDrive 现在的做法，列出我们要做的方式。
依据：各家官方文档与帮助页（文末来源）+ ZyDrive 自身代码
（`internal/app/oauth.go`、`internal/app/builtin_oauth.go`、
`docs/oauth-apps/README.md`）。

## 一、总览对比

| 维度 | Air Explorer | rclone | Alist / OpenList | Cyberduck / Mountain Duck | ZyDrive（现在） |
|---|---|---|---|---|---|
| 内置共享客户端 | 有（点添加账号、浏览器授权即用） | 有（每后端内置 client_id，可被覆盖） | 部分有（OpenList 内置密钥对，只覆盖部分驱动；Google Drive 没有） | 有（每家注册好的客户端） | 有（Dropbox / Drive / OneDrive 三家，Client ID 写进仓库 JSON） |
| 自建应用 | 有（Google Drive Custom Client ID，需填 ID + Secret） | 有（配置文件里填自己的 client_id / client_secret） | 有（表单里填自己的 Client ID / Secret，留空用内置） | 有（可注册自己的 Google OAuth 客户端，独立于官方的） | 有（向导第 2 步并排卡片；Secret 只存本机加密） |
| 回调方式 | 系统浏览器授权，应用内接回 | 本地 127.0.0.1:53682 监听；另有 localhost.rclone.org、https://oauth.rclone.org/ 中转、urn:ietf:wg:oauth:2.0:oob 手动粘贴兜底 | 不走应用内回调：去外部取令牌站 api.oplist.org 拿 refresh_token，粘贴进表单；部分驱动扫码登录 | 自定义 scheme（`x-cyberduck-action:oauth` / `x-mountainduck-action:oauth`）；也支持 `http://localhost` 回环 | 127.0.0.1 随机端口回环监听、自动取码；手动粘贴为兜底 |
| PKCE | 未公开说明 | 依赖各后端（OAuth2 标准流程） | 依赖各驱动 | 有（8.5.5 起，Google / Dropbox） | 有（S256，全部 OAuth 驱动） |
| 令牌存储 | 本地加密存放 | rclone.conf 明文（文档明确警告）；可选整文件密码加密；密码只做可逆混淆 | 数据库明文存放粘贴进来的 token | 系统钥匙串（macOS Keychain / Windows Credential Manager），按账号分开存；过期时间也进钥匙串 | SQLite，master.key 加密（PBKDF2 + AES-256-GCM），按 Mount 隔离 |
| 多账号 | 支持（免费版每云 1 个，Pro 不限） | 配置文件里多个 remote | 多个存储挂载 | 一个书签一个账号；授权新账号前需先在浏览器退出当前账号，避免绑错号 | 支持（多账号按 Mount 隔离，各走各的 Token） |
| 无头 / 服务端方案 | 无（纯桌面） | `rclone authorize`（桌面跑授权、把 token 粘回服务器）；或直接拷配置文件；或 SSH 隧道转 53682 端口 | 无（网页版跑在服务器上，令牌站取 token 后粘贴） | CLI 不能走 OAuth：桌面先授权一次，CLI 复用钥匙串里的 token | 加密配置包导出 / 导入（桌面授权、人工拷包到服务器；不经过我们的服务器） |
| 刷新策略 | 未公开说明 | TokenSource 自动刷新，刷新后写回配置文件 | 懒刷新：请求时发现过期才刷 | 自动刷新；refresh token 因长期不用过期后需重新授权 | Token() 时检查过期自动刷；刷失败标 auth_expired，引导重新授权 |
| 共享客户端的风险 | 未公开说明 | 共享 client_id 有配额（Google Drive 约 750GB/天），超了会限流；自建可解除 | 内置密钥对是单点（公益服务器限流、滥用会被封） | 未公开说明 | Dropbox 开发态 500 用户上限；Google 测试态 refresh token 7 天过期（见下） |

## 二、各家要点

### Air Explorer
- 默认就是一键授权：选网盘 → 浏览器打开授权页 → 点允许 → 云盘直接出现；不存用户密码，token 可随时在各家安全设置里撤销。
- Google Drive 另有“Custom Client ID”入口：用户在 Google Cloud 自建应用、填 ID + Secret，再走浏览器授权。内置与自建并存，与 ZyDrive 的双卡片是同一思路。
- 账号可导出为密码保护的文件、到另一台电脑导入（与 ZyDrive 的 Agent 配置包同类做法）。

### rclone
- 授权码流程 + 本地监听是最完整的参考实现：固定端口 53682（需在应用设置里预先登记该回调地址）、`localhost.rclone.org`、公网中转 `oauth.rclone.org`、手动粘贴 `oob` 四种回调全有。
- 无头方案最完整：`rclone authorize` 是标准答案（桌面授权、token 粘回服务器），其次是拷配置文件、SSH 隧道。
- 令牌存储是反面教材：默认明文，只靠文件权限与可选的整文件密码保护；官方文档明确警告 refresh_token 泄露即等于账号泄露。

### Alist / OpenList
- 不做应用内浏览器授权：授权发生在外部取令牌站（api.oplist.org），用户在站上选“用 OpenList 提供的参数”（内置密钥对）或填自己的 ID / Secret，扫码或登录后把 refresh_token 复制、粘贴进存储表单。
- Google Drive 没有内置客户端，用户必须自建 Google 应用（开 API、同意屏幕、建 OAuth 客户端）。
- 令牌明文存数据库；刷新是懒刷新。

### Cyberduck / Mountain Duck
- 回调用自定义 scheme（`x-cyberduck-action:oauth`），浏览器授权后点“打开 Cyberduck”把授权码交回应用；也支持 `http://localhost` 回环（Entra 对 localhost 忽略端口）。
- 令牌存系统钥匙串、按账号分开，是四家里的存储标杆；OAuth 过期时间也进钥匙串。
- CLI 不能走 OAuth：必须先在桌面应用里授权一次，CLI 复用钥匙串里的 token——与 ZyDrive“桌面授权、Agent 复用”的思路完全一致，只是他们用钥匙串、我们用加密配置包。
- 支持自建 Google OAuth 客户端；PKCE 从 8.5.5 起覆盖 Google / Dropbox。

### 标准：RFC 8252（OAuth 2.0 for Native Apps）
- 桌面/手机应用是 public client，没有 secret 可守，PKCE（S256）是必须的，不是可选的。
- 必须用系统浏览器授权，禁止内嵌 WebView（应用能读到用户输入的密码，且厂商会拒绝渲染）。
- 三种回调：自定义 scheme、私有 scheme、回环 `127.0.0.1`（端口可变，是唯一允许 redirect_uri 不精确匹配的例外）；`127.0.0.1` 优先于 `localhost`（后者在某些系统上解析到 `::1`，监听收不到）。

## 三、ZyDrive 现在的方式（已落地）

- 双轨：内置共享客户端（Dropbox / Drive / OneDrive，Client ID 在仓库
  `internal/app/builtin-oauth-clients.json`，go:embed，代码常量兜底）
  + 自己的应用（`~/.config/zy-drive/custom-oauth-clients.json`，Secret 经
  master.key 加密；两者分开存放、随时切回内置）。
- 回调：127.0.0.1 随机端口回环监听、自动取码；手动粘贴为兜底；Box 固定
  57103、pCloud 固定 57104（两家要求回调地址精确匹配）。
- PKCE S256 全部 OAuth 驱动开启；Box / pCloud 因换 token 必须带 Secret，
  永远没有一键授权（文档已写明）。
- 令牌：SQLite + master.key 加密，按 Mount 隔离；刷新失败标
  `auth_expired`，前端引导去设置里重新授权；文件浏览重试走 TestMount。
- 无头 Agent：桌面导出加密配置包，用户自拷服务器导入，不经过我们的服务器。

## 四、我们要做的方式

| 事项 | 现状 | 目标 | 优先级 |
|---|---|---|---|
| 内置应用转生产 | Dropbox 开发态（500 用户上限）、Google 测试态（refresh token 7 天过期）、OneDrive 首次授权“未经验证”提示 | Dropbox 点 Apply for production；Google 走验证（需域名、演示视频）；OneDrive 保持现状 | P1（给别人用之前必须做） |
| 共享客户端失效时引导切自建 | 两张卡片并排，但内置授权失败时没有明确指引 | 授权失败页写明原因 + “改用自己的应用”一键跳转（已在 README 里记了风险，缺界面上的引导） | P1 |
| `zy-drive authorize` 命令 | 无头 Agent 只靠人工拷加密配置包 | 仿 rclone authorize / Cyberduck CLI 模式：桌面跑授权、输出一次性 token、Agent 端粘贴导入；比拷包顺滑 | P2 |
| 令牌存钥匙串 | SQLite + master.key 加密（已比 rclone / Alist 明文强） | 桌面端改存系统钥匙串（macOS Keychain / Windows Credential Manager），按账号分开；master.key 方案保留为跨平台兜底 | P2（Windows 移植时一起做） |
| 回调地址写 127.0.0.1 | 监听绑 127.0.0.1，但 redirect_uri 写 `http://localhost:<端口>` | 核实并统一成 `http://127.0.0.1:<端口>`（RFC 8252 建议，避免 localhost 解析到 ::1 收不到回调） | P2（小改动，先核实再改） |
| 授权新账号前提醒退登 | 无 | 仿 Cyberduck：在向导里提醒“加第二个同种账号前，先在浏览器退出当前账号”，避免绑错号 | P2（文案级） |
| 自定义 scheme | 无 | 桌面保持回环不动；移动端以后再加 `zydrive://oauth`（RFC 8252 7.1），现在不做 | P3（移动端时） |
| 设备码流（device code） | 无 | 评估：Google / Microsoft 支持、Dropbox 不支持；只在 `zy-drive authorize` 不够用时再考虑 | P3（待评估） |
| 权限范围最小化 | 各家只申请读写必需 scope | 保持；新驱动接入时在对照表里登记 scope，不多要 | 持续 |

## 五、新网盘接入时的授权登记（加进接入清单）

每个新驱动除功能字段外，必须登记：授权方式（OAuth / 密钥 / 密码 / 扫码）、
内置客户端有无、是否需 Secret（决定有无一键授权）、回调方式与固定端口、
PKCE 有无、scope 清单、refresh token 有效期与过期表现、令牌存储位置、
无头方案。

## 来源

- Air Explorer 帮助与 Custom Client ID 指南：
  https://www.airexplorer.net/en/help/ 、
  https://www.airlivedrive.com/en/setup-a-custom-oauth-client-id-for-google-drive/
- rclone remote_setup 与 oauthutil：
  https://rclone.org/remote_setup/ 、
  https://pkg.go.dev/github.com/rclone/rclone/lib/oauthutil
- rclone 配置加密与 token 明文警告：
  https://rclone.org/hidrive/ （Keeping your tokens safe）
- OpenList Aliyundrive Open 文档：
  https://migrate-v3.openlist-docs-next.pages.dev/guide/drivers/aliyundrive_open
- Cyberduck Google Drive / Google Cloud Storage / Azure 文档：
  https://docs.cyberduck.io/protocols/googledrive/ 、
  https://docs.cyberduck.io/protocols/googlecloudstorage/ 、
  https://docs.cyberduck.io/protocols/azure/
- RFC 8252 解读：
  https://etelej.com/lab/oauth2/specs/rfc-8252/ 、
  https://docs.intelliauth.io/developer/oauth/authorization-code-pkce-native
