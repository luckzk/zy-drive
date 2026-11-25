# Dropbox：注册 ZyDrive-app

> 总览见 [README](README.md)。这里只讲 Dropbox。文档里只写占位符，不写
> 任何真实 Client ID、Secret、Token 的值。

## 准备

- 一个 Dropbox 账号（个人账号即可）。
- 打开 [Dropbox App Console](https://www.dropbox.com/developers/apps)。

## 步骤

### 1. 创建应用

1. 点 **Create app**。
2. **Choose an API**：选 **Scoped access**（不要选 App folder 那种旧模式）。
3. **Choose the type of access you need**：选 **Full Dropbox**（完整访问整个
   Dropbox）。
   - 不要选“应用文件夹”：那样应用只能看到它自己的文件夹，同步接口会
     找不到用户的其他文件，而且建好之后改不了。
4. **Name your app**：填 `ZyDrive-app`。
   - 应用名全局唯一：如果提示已被占用，在后面加后缀，例如 `ZyDrive-app-2`。
5. 点 **Create app**。

### 2. 勾权限并提交

1. 进新建应用的 **Permissions** 页。
2. 勾选以下四项：
   - `files.metadata.read`
   - `files.metadata.write`
   - `files.content.read`
   - `files.content.write`
3. 点页面下方的 **Submit**。

### 3. 复制 App key（就是 Client ID）

1. 进 **Settings** 页。
2. 找到 **App key**，复制它——这就是 Client ID。
   - **App secret 不要复制、不要外传**：ZyDrive 官方应用是公开客户端，
     用 PKCE，不用 Secret。代码里也没有 Secret 字段。
3. 把 App key 填回 ZyDrive 仓库文件 `internal/app/builtin-oauth-clients.json`
   的 `dropbox` 那一行（同时同步 `internal/app/builtin_oauth.go` 里的常量
   兜底值），重新构建。

### 4. 加重定向 URI

1. 仍在 **Settings** 页，找到 **OAuth 2** → **Redirect URIs**。
2. 输入 `http://localhost`，点 **Add**。
   - 只填裸 `http://localhost`（不带端口）：ZyDrive 运行时用随机端口，
     Dropbox 对 `http://localhost` 按回环惯例放行。
   - 注意：如果填了带端口的地址（如 `http://localhost:53682`），授权时会
     报 `invalid_redirect_uri`——因为运行时端口是随机的，对不上。
3. 确认列表里出现了 `http://localhost`（填了不点 Add 等于没加）。

### 5. 打开“允许公共客户端”

1. 仍在 **Settings** 页，找到 **Allow public clients**（允许公共客户端）
   开关，打开它。
   - 不打开的话，公开客户端 + PKCE 的授权会被 Dropbox 拒绝。

## 状态与上限

- 刚建好是**开发状态**：关联用户上限 **500 个**。
- 给更多人用之前，在应用页点 **Apply for production** 升级成生产状态，
  解除上限。
- 关联用户数与状态都在 App Console 的应用页上能看到。

## 验证

1. 重新构建 ZyDrive，添加网盘选 Dropbox，第 2 步选“官方应用（一键授权）”，
   点“打开浏览器授权”。
2. 浏览器里登录 Dropbox 点允许，应自动跳回 ZyDrive、自动换到 Token，
   不用手动复制 code。
3. 如果浏览器没跳回：从地址栏复制 `code=` 后面的值，粘贴回“授权码”栏，
   点“换取 Token”。
4. 若授权页直接报错 `invalid_redirect_uri`：回第 4 步检查 Redirect URIs
   里是否已添加 `http://localhost`。
