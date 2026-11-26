<!-- SPDX-License-Identifier: MPL-2.0 -->
# Box：注册 ZyDrive-app

> 总览见 [README](README.md)。这里只讲 Box。文档里只写占位符，不写
> 任何真实 Client ID、Secret、Token 的值。

## 准备

- 一个 Box 账号（个人账号即可）。
- 打开 [Box 开发者控制台](https://app.box.com/developers/console)。

## 步骤

### 1. 创建应用

1. 点 **Create New App**。
2. 选 **Custom App**，名字填 `ZyDrive-app`。
3. **Choose Authentication Method**：选 **User Authentication (OAuth 2.0)**。
   - 不要选 **Server Authentication**（JWT / 服务账号）：那是另一套
     认证方式，ZyDrive 不走那条路。
4. 点 **Create App**。

### 2. 填重定向 URI（固定端口）

1. 进新建应用的 **Configuration** 页。
2. 找到 **OAuth 2.0 Redirect URI**，输入 `http://localhost:57103`，
   点 **Add**。
   - 必须带端口 `57103`：Box 不像 Dropbox 那样对裸 `http://localhost`
     放行；ZyDrive 固定监听 57103，所以这里填死这个端口。
   - 注意：如果填了别的端口，授权时会报 `invalid_redirect_uri`——
     因为回调只监听 57103。

### 3. 勾权限

1. 仍在 **Configuration** 页，找到 **Application Scopes**。
2. 勾选 **Read and write all files and folders stored in Box**
   （`root_readwrite`）。
   - 只读权限不够：ZyDrive 要上传 / 改名 / 移动，缺写权限会报 403。
3. 改完点页面下方的 **Save Changes**。

### 4. 复制 Client ID 和 Client Secret

1. 仍在 **Configuration** 页，找到 **OAuth 2.0 Credentials**。
2. 复制 **Client ID** 和 **Client Secret**。
   - **和 Dropbox / OneDrive 不一样：Box 换 Token 必须带 Client Secret。**
     Box 没有“公开客户端 + PKCE 不用 Secret”这条路，所以 ZyDrive
     里 Box 没有一键授权，只能用自己的应用，ID 和 Secret 都要填。
   - Secret 只存在本机（加密存储），不上传、不进仓库。

### 5. 为什么没有一键授权

Box 的 Token 端点要求 Client Secret，而 ZyDrive 的官方共享应用只
共享 Client ID（Secret 不能公开）。所以 Box 的官方应用无法一键授权，
向导里只会出现“自己的应用”；上面建好的应用就是这台机器要用的。

## 状态与上限

- 刚建好是**开发状态**：只能给创建者自己的账号授权，自用没问题。
- 给更多人用之前，要把应用提交 Box 审核、发布成生产应用（控制台
  里有 Publish / 提交入口），否则别人的账号授权会被拒。

## 验证

1. 重新构建 ZyDrive，添加网盘选 Box，第 2 步选“自己的应用”，填上面
   拿到的 Client ID 和 Client Secret，点“生成授权链接”。
2. 浏览器里登录 Box 点允许，应自动跳回 ZyDrive、自动换到 Token，
   不用手动复制 code。
3. 如果浏览器没跳回：从地址栏复制 `code=` 后面的值，粘贴回“授权码”栏，
   点“换取 Token”。
4. 若授权页直接报错 `invalid_redirect_uri`：回第 2 步检查 Redirect URI
   里是否已添加 `http://localhost:57103`（带端口）。
5. 若换 Token 报错 `invalid_client`：回第 4 步检查 Client Secret 是否
   填对（Secret 区分大小写，复制时别带空格）。
