# Google Drive：注册 ZyDrive-app

> 总览见 [README](README.md)。这里只讲 Google Drive。文档里只写占位符，
> 不写任何真实 Client ID、Secret、Token 的值。

## 准备

- 一个 Google 账号（个人账号即可）。
- 打开 [Google Cloud Console](https://console.cloud.google.com/)。

## 步骤

### 1. 新建项目

1. 顶部项目下拉 → **新建项目**。
2. 项目名称填 `ZyDrive`，点**创建**，等项目建好后切到这个项目。

### 2. 启用 Google Drive API

1. 左侧 **API 和服务** → **库**。
2. 搜索 `Google Drive API`，点进去，点**启用**。

### 3. 配置 OAuth 同意屏幕

1. 左侧 **API 和服务** → **OAuth 同意屏幕**。
2. **User Type** 选**外部**，点**创建**。
3. 填应用信息：
   - **应用名称**：`ZyDrive-app`
   - **用户支持电子邮件**：填你自己的邮箱
   - **开发者联系信息**：填你自己的邮箱
4. 点**保存并继续**。
5. **范围**页先不用加，点**保存并继续**（权限在第 4 步的数据访问里勾）。

### 4. 目标受众：把自己加为测试用户

1. 在 OAuth 同意屏幕的**目标受众**（Audience）页，点**添加用户**。
2. 输入你自己的 Google 账号邮箱，点**添加**。
   - 测试状态下，只有加为测试用户的账号能授权。以后发布应用后就不用了。

### 5. 数据访问：勾 Drive 权限

1. 在 OAuth 同意屏幕的**数据访问**（Data Access）页，点**添加或移除范围**。
2. 搜索并勾选 `https://www.googleapis.com/auth/drive`（完整访问 Drive）。
3. 点**更新**，再点**保存并继续**。

### 6. 创建 OAuth 客户端 ID（桌面应用）

1. 左侧 **API 和服务** → **凭据**。
2. 点 **+ 创建凭据** → **OAuth 客户端 ID**。
3. **应用类型**选**桌面应用**。
4. 名称填 `ZyDrive-app`，点**创建**。
5. 弹出的窗口里复制 **客户端 ID**（Client ID）。
   - 客户端密钥（Client Secret）不用复制：ZyDrive 官方应用是公开客户端，
     用 PKCE，不用 Secret。代码里也没有 Secret 字段。
   - 桌面应用**不用填重定向 URI**：Google 默认允许 `http://localhost`，
     ZyDrive 运行时用随机端口也能跳回。
6. 把客户端 ID 填回 ZyDrive 仓库文件 `internal/app/builtin-oauth-clients.json`
   的 `drive` 那一行（同时同步 `internal/app/builtin_oauth.go` 里的常量
   兜底值），重新构建。

## 状态与上限

- 刚建好是**测试状态**：
  - 只有测试用户能授权。
  - Refresh Token **7 天过期**（过期后用户要重新授权）。
- **发布应用**：在 OAuth 同意屏幕点**发布应用**，要走 Google 验证。Drive
  完整权限（`https://www.googleapis.com/auth/drive`）属于敏感范围，审核
  要提供应用说明与演示视频，通过后 Refresh Token 不再 7 天过期。
- 给别人用之前，先决定是保持测试状态（只给自己和少数测试用户），还是
  走发布验证。

## 验证

1. 重新构建 ZyDrive，添加网盘选 Google Drive，第 2 步选“官方应用（一键授权）”，
   点“打开浏览器授权”。
2. 浏览器里登录 Google 账号（必须是测试用户）点允许，应自动跳回 ZyDrive、
   自动换到 Token，不用手动复制 code。
3. 如果浏览器没跳回：从地址栏复制 `code=` 后面的值，粘贴回“授权码”栏，
   点“换取 Token”。
4. 若授权页报“此应用未经验证”：这是测试状态的正常提示，点**高级** →
   **前往 ZyDrive-app（不安全）**继续；发布验证通过后不再出现。
5. 若授权页报“access_blocked：此应用的请求被屏蔽”：登录的账号没加为测试
   用户，回第 4 步加。
