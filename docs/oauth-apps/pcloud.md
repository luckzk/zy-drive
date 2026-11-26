<!-- SPDX-License-Identifier: MPL-2.0 -->
# pCloud：注册 ZyDrive-app

> 总览见 [README](README.md)。这里只讲 pCloud。文档里只写占位符，不写
> 任何真实 Client ID、Secret、Token 的值。

## 准备

- 一个 pCloud 账号（个人账号即可）。
- 打开 [pCloud API 文档站](https://docs.pcloud.com/)，点 **Create App**
  （要先登录 pCloud 账号）。

## 步骤

### 1. 创建应用

1. 点 **Create App**，名字填 `ZyDrive-app`。
2. **Redirect URIs** 里加一行 `http://localhost:57104`。
   - 必须带端口 `57104`：ZyDrive 固定监听 57104，回调只走这个端口；
     填别的端口授权时会报重定向不匹配。
3. 点创建，拿到 **Client ID** 和 **Client Secret**。
   - **和 Dropbox / OneDrive 不一样：pCloud 换 Token 必须带 Client Secret。**
     pCloud 没有“公开客户端 + PKCE 不用 Secret”这条路，所以 ZyDrive
     里 pCloud 没有一键授权，只能用自己的应用，ID 和 Secret 都要填。
   - Secret 只存在本机（加密存储），不上传、不进仓库。

### 2. 为什么没有一键授权

pCloud 的 Token 端点要求 Client Secret，而 ZyDrive 的官方共享应用只
共享 Client ID（Secret 不能公开）。所以 pCloud 的官方应用无法一键授权，
向导里只会出现“自己的应用”；上面建好的应用就是这台机器要用的。

## 美区 / 欧区

pCloud 账号分两个数据中心：

- **美区**：`api.pcloud.com`（授权页 `my.pcloud.com`）
- **欧区**：`eapi.pcloud.com`（授权页 `e.pcloud.com`）

注册应用时不用选区——应用本身不分美欧。**用的时候**在 ZyDrive
向导里选区：

- **自动（先试美区）**：默认。先按美区授权；如果你的账号在欧洲，
  换 Token 时会失败，改选“欧洲”再重新生成链接。
- **美国 / 欧洲**：直接固定用对应的 host。
- 账号在哪个区，看 pCloud 网页版地址栏：`my.pcloud.com` 是美区，
  `e.pcloud.com` 是欧区。

> 技术细节：驱动的自动识别是“先试美区 userinfo，失败再试欧区”
> （`internal/storage/providers/pcloud`）。欧区的 Token 端点
> `https://eapi.pcloud.com/oauth2_token` 是按 host 对称性推断的，
> 尚未在真实欧洲账号上验证过；如果欧洲账号换 Token 失败，先在
> 向导里确认选了“欧洲”，再看报错。

## 验证

1. 重新构建 ZyDrive，添加网盘选 pCloud，第 2 步选“自己的应用”，填上面
   拿到的 Client ID 和 Client Secret，选好账号所在区，点“生成授权链接”。
2. 浏览器里登录 pCloud 点允许，应自动跳回 ZyDrive、自动换到 Token，
   不用手动复制 code。
3. 如果浏览器没跳回：从地址栏复制 `code=` 后面的值，粘贴回“授权码”栏，
   点“换取 Token”。
4. 若授权页直接报错重定向不匹配：回第 1 步检查 Redirect URIs 里是否
   已添加 `http://localhost:57104`（带端口）。
5. 若换 Token 失败但授权页能正常打开：大概率是区选错了——改选另一个区，
   重新生成链接再试一次（不要复用旧 code，code 只能用一次）。
6. pCloud 的 Token 长期有效，没有 Refresh Token：向导里“直接粘贴”
   那栏只用填 Access Token。
