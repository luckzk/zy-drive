# 注册 ZyDrive 官方 OAuth 应用

> 这份文档写给要给 ZyDrive 注册（或更换）官方 OAuth 应用的人。三个应用
> 名统一叫 **ZyDrive-app**。文档里只写占位符，不写任何真实 Client ID、
> Secret、Token 的值。

## 为什么要注册官方应用

ZyDrive 添加 Dropbox、Google Drive、OneDrive 时，要在各家的控制台里有一
个“应用”（OAuth 客户端），用户点“允许”授权自己的账号。没有这个应用，
每个用户都得自己去控制台建一个应用、填一堆字段——对新手极不友好。

官方应用就是 ZyDrive 自己的三个应用，由维护者一次性注册好、把 Client ID
写进仓库。以后所有用户添加网盘时，直接点“一键授权”就行，不用自己建应用：

- [Dropbox](dropbox.md)
- [Google Drive](google-drive.md)
- [OneDrive](onedrive.md)

## Client ID 是公开标识，Client Secret 不是

- **Client ID 是公开标识**：它只说明“这是哪个应用”，出现在授权链接里，
  人人可见。所以它可以写进仓库、写进文档。
- **Client Secret 不要外传**：有 Secret 的应用（Dropbox / Google Drive 自建
  应用）才有它。ZyDrive 的三个官方应用都不用 Secret（公开客户端 + PKCE），
  代码里也没有 Secret 字段。用户自己建的应用如果有 Secret，只存在用户本
  机、走主密钥加密，绝不明文存放、不进仓库。

## 官方应用 vs 自己的应用

添加网盘的第 2 步有两个并排的选项卡片：

| | 官方应用（一键授权） | 自己的应用 |
|---|---|---|
| 是什么 | ZyDrive 注册好的共享应用 | 用户自己在控制台建的应用 |
| 要用户做什么 | 不用建应用，点授权就好 | 自己去控制台建一次，填 Client ID（Secret 如有也填），可点“保存为我的应用” |
| 存在哪 | 仓库 `internal/app/builtin-oauth-clients.json`（公开 Client ID），随程序发版 | 用户本机 `~/.config/zy-drive/custom-oauth-clients.json`（Secret 加密） |
| 风险 | 共享应用是单点故障：如果厂商限流、标记或停用 ZyDrive 这个应用，所有用户的一键授权同时失效 | 自己的应用额度与信誉是自己的，不受 ZyDrive 应用影响 |

两者永远分开存放：用自己的应用不会覆盖内置的官方应用，切回“官方应用”
一点就回来。旧版曾用过的 `~/.config/zy-drive/oauth-clients.json`（覆盖式）
已废弃，启动时自动迁移进“自己的应用”，再也不作为覆盖生效。

## 注册好后填回 ZyDrive 的哪里

1. 把拿到的 Client ID 写进仓库文件 `internal/app/builtin-oauth-clients.json`
   对应网盘的那一行（文件头有注释说明）。改这里、重新构建，一键授权就
   用新应用。
2. 文件读不到或格式坏了时，程序回落到 `internal/app/builtin_oauth.go` 里
   的代码常量兜底——改 JSON 的同时也同步改那里的常量，两处保持一致。
3. 重新构建、验证一键授权能走通，再推送。

## 测试状态与发布验证

三家刚注册好的应用都在“测试 / 开发”状态，有各自的限制，正式给别人用
之前要处理：

- **Dropbox**：开发状态，关联用户上限 500 个。以后点 **Apply for production**
  升级成生产状态，解除上限。
- **Google Drive**：OAuth 同意屏幕在测试状态时，只有加为测试用户的账号能
  授权，且 Refresh Token **7 天过期**。发布应用要走 Google 验证（Drive 完
  整权限属于敏感范围，审核要提供说明与演示视频）。
- **OneDrive**：个人账号注册即可；Azure 上一般不用单独“发布”，但首次授权
  时用户会看到“未经验证的应用”类提示，点继续即可。

细节与每一步截图要点见各家文档：[Dropbox](dropbox.md)、
[Google Drive](google-drive.md)、[OneDrive](onedrive.md)。
