# 添加网盘方式对比（2026-11-26）

调查范围：Air Explorer、rclone、Alist / OpenList、Cyberduck / Mountain Duck、
ExpanDrive、Pail，对照 ZyDrive 现在的添加流程，列出我们要做的方式。
依据：各家官方文档与帮助页（文末来源）+ ZyDrive 自身代码
（`frontend/src/components/AddMountDialog.tsx`，1783 行）与
`docs/oauth-comparison.md`（授权方式已另文对比）。

## 一、总览对比

| 维度 | Air Explorer | rclone | Alist / OpenList | Cyberduck / Mountain Duck | ExpanDrive | ZyDrive（现在） |
|---|---|---|---|---|---|---|
| 入口 | Accounts → Add account | 命令行 `rclone config` → n) New remote | 网页管理端 Storages → Add | Open Connection / New Bookmark | Add Connection | 侧栏“新建网盘”按钮 |
| 选网盘的方式 | 图标列表 | 数字编号列表（70+ 后端，按号选） | 下拉框选 driver | 下拉框选协议 | 列表选服务 | 9 卡片图标网格 |
| 流程结构 | 单表单：选图标 → 填表 → OK | 命令行逐项问答（client_id、scope、root 等；高级项藏在 "Edit advanced config?" 后面） | 单表单：选 driver → 按字段填表 → Save | 单表单：选协议 → 填服务器/凭证 → Connect | 选服务 → 授权/填服务器 → 选要挂的盘（My Drive / Shared Drive）→ 选挂载点 → Connect | 向导：OAuth 3 步（选盘 → 授权 → 确认），密钥类 2 步（选盘 → 表单，无确认页） |
| OAuth 授权 | 点添加 → 浏览器授权；Google Drive 另有 Custom Client ID 入口 | 浏览器自动授权（auto config Y/N）；无头用 `rclone authorize` | 不走应用内授权：去取令牌站拿 refresh_token 粘贴进表单 | 浏览器授权，自定义 scheme 交回应用；一个书签一个账号 | 浏览器 OAuth 授权 | 官方应用一键授权 / 自己的应用两张并排卡片；回环自动取码，手动粘贴兜底 |
| 密钥类表单 | S3：REST endpoint + Access Key ID + Secret + Bucket（不填 Bucket 则显示全部桶）；Path style 开关 | 逐项问答，空 client_id/secret 即用内置 | 各 driver 自己的字段（如 Root Folder ID、refresh_token） | 服务器地址 + 用户名/密码或密钥 | 主机名/端口/用户名/密码或 SSH 密钥 | 单页表单：字段 + “测试连接”+“保存”同页；Drime 有工作区选择器，pCloud 有分区选择器 |
| 测试 / 验证 | 未公开说明（填完即保存） | 无独立测试步骤；token 换取成功即完成，问题在使用时暴露 | 无保存前测试；保存后状态列显示 work / fail | Connect 本身就是测试：连不上就停在登录 | Connect 即测试 | 表单内测试：通过存为“已验证”；没测或失败也能保存，标“未验证”（侧栏琥珀徽标，设置页可重测） |
| 高级项放哪 | 每个云自己的配置窗口里 | "Edit advanced config?" 问答后面 | 表单下方的可选字段 | 连接配置（.cyberduckprofile 可预置） | 连接设置里（缓存等） | “高级”折叠区；自建应用、固定端口等只在需要时出现 |
| 命名 / 分组 / 根目录 | 账号名可改；可建账号分组 | remote 名即标识；root_folder_id 限定根目录 | Mount path（挂载路径）+ Root Folder ID | 书签昵称；每个书签独立 | 连接昵称 + 挂载点（盘符/目录） | 名称 + 分组（新建分组/选分组）；根目录由驱动定 |
| 第二个同种账号 | 再添加一个（免费版每云限 1 个，Pro 不限） | 再建一个 remote，换个名字 | 再加一个存储，换个挂载路径 | 再建一个书签（授权前需先在浏览器退出当前账号，避免绑错） | 再加一个连接，各有挂载点 | 再走一遍向导（OAuth 授权前提醒退登，见授权对比文 P2） |
| 改 / 重授权 | Accounts 里编辑 | `rclone config` 改 remote；token 过期自动刷 | 存储列表里编辑，改完保存 | 书签编辑；token 删钥匙串条目即重置 | 连接列表里编辑 | 设置 → 网盘 → 重新测试 / 重新授权 |
| 失败提示 | 未公开说明 | 命令行直接报错文本 | 状态列 fail，无详细原因 | 登录窗口报错 | 连接窗口报错 | 人话错误（区分网络/授权/配置三类） |

## 二、各家要点

### Air Explorer
- 添加就是“选图标 → 填表 → OK”，没有向导步数、没有确认页、没有单独的测试按钮。
- S3 类表单最简：endpoint + 两把 key + 可选 bucket；AWS、Backblaze 有自己的图标，
  其它 S3 兼容服务走通用 S3 图标；Path style 是个开关。

### rclone
- 添加全程命令行问答：先给 remote 起名，再从编号列表选后端，再逐项回答；
  client_id / client_secret 留空即用 rclone 内置的；高级选项默认不问。
- 没有“测试连接”按钮：OAuth 换 token 成功就算配好，连通性问题在使用时才暴露。

### Alist / OpenList
- 添加是管理端的一个表单：下拉选 driver，按字段填，refresh_token 从取令牌站粘贴进来；
  Client ID / Secret 留空即用 OpenList 内置的。
- 没有保存前测试；保存后靠状态列（work / fail）判断，失败原因不展开。

### Cyberduck / Mountain Duck
- 添加即连接：填好点 Connect，浏览器授权，成功后书签自动存下来；Connect 本身就是测试。
- 一个书签就是一个账号；多账号靠多个书签；自定义连接配置（.cyberduckprofile）
  可以预置服务器与 OAuth 参数。

### ExpanDrive
- 添加分两段：先认证（OAuth 或服务器字段），再选“挂哪个盘”（My Drive / Shared Drive）
  与挂载点，最后 Connect；挂上立刻出现在 Finder / 资源管理器里。
- 每个连接有自己的昵称与挂载点；也有命令行 `expandrive mount`。

### Pail
- 只有 S3：一个表单（endpoint / keys / bucket），无向导、无 OAuth。

## 三、ZyDrive 现在的方式（已落地，代码核实）

- 入口：侧栏“新建网盘”按钮，打开 `AddMountDialog`（1783 行）。
- 第 1 步：9 卡片图标网格选网盘；第 2 步：OAuth 两张并排卡片
  （官方应用一键授权 / 自己的应用）或密钥类单页表单；
  第 3 步：仅 OAuth 有，是确认页。
- 密钥类表单：字段 + “测试连接” + “保存”同页；测试通过存为已验证，
  没测或失败也能保存，标“未验证”，侧栏琥珀徽标，设置 → 网盘可重测。
- 网盘特有的选择放在添加流程里，不藏进设置：Drime 工作区选择器、
  pCloud 美区/欧区选择器。
- 每家 OAuth 帮助（OAUTH_HELP）写在向导里；错误分网络/授权/配置三类人话提示。

## 四、我们要做的方式

| 事项 | 现状 | 目标 | 优先级 |
|---|---|---|---|
| 评估去掉第 3 步确认页 | OAuth 有 3 步，密钥类 2 步；四家竞品都没有确认页 | 确认页只在“信息值得再看一眼”时保留，否则测试通过直接保存；先在向导里统计用户在确认页的停留与回退，再定 | P1（评估） |
| 选盘网格加搜索 | 9 卡片网格，无搜索 | 网盘数过 12 个后加搜索框（rclone/Alist 的列表在 70+ 时靠搜索/编号，网格不加搜索会越翻越长） | P1（随加盘数量触发） |
| 保存“未验证”的徽标更显眼 | 侧栏琥珀徽标 + 设置页重测 | 徽标旁加“一键重测”入口（现在要点进设置页才能重测） | P2 |
| 加第二个同种账号前提醒退登 | 无（已记在授权对比文 P2） | 向导第 2 步文案提醒 | P2（文案级） |
| 命令行添加 | 只有 `cmd/cli` 骨架 | `zy-drive add`（配合授权对比文的 `zy-drive authorize`），给 Agent/无头场景用 | P3 |
| 预置配置（连接模板） | 无 | 仿 Cyberduck profile：常用 S3 兼容服务（R2、Backblaze、Wasabi）给预置 endpoint/region，用户只填 key | P3 |

## 来源

- Air Explorer 帮助与 S3 指南：
  https://www.airexplorer.net/en/help/ 、
  https://www.airexplorer.net/en/store-your-data-in-the-cloud-with-s3/ 、
  https://www.airexplorer.net/en/how-to-sync-and-manage-your-files-on-digitalocean/
- rclone remote_setup：https://rclone.org/remote_setup/
- OpenList 快速上手与 Aliyundrive Open 文档：
  https://support.ugnas.com/detail/article/en-US/763 、
  https://migrate-v3.openlist-docs-next.pages.dev/guide/drivers/aliyundrive_open
- Cyberduck Google Drive 文档：
  https://docs.cyberduck.io/protocols/googledrive/
- ExpanDrive Google Drive 挂载指南：
  https://www.expandrive.com/blog/map-google-drive-as-a-network-drive
- FolderSpan S3 表单（测试连接按钮的参考）：
  https://www.folderspan.com/en/docs/tutorial-extras/network/S3/
