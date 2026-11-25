# OneDrive：注册 ZyDrive-app

> 总览见 [README](README.md)。这里只讲 OneDrive。文档里只写占位符，不写
> 任何真实 Client ID、Secret、Token 的值。

## 准备

- 一个 Microsoft 账号（**个人账号即可**，不用开发者账号、不用买订阅；
  工作 / 学校账号也可以，但组织管理员可能限制应用注册）。
- 打开 [Azure 门户](https://portal.azure.com/)。

## 步骤

### 1. 新注册应用

1. 左侧找到 **Microsoft Entra ID**（旧名 Azure Active Directory），点进去。
2. 左侧 **管理** → **应用注册**，点 **+ 新注册**。
3. **名称**填 `ZyDrive-app`。
4. **受支持的帐户类型**选**任何组织目录中的帐户和个人 Microsoft 帐户**
   （Any organizational directory + personal Microsoft accounts）。
   - 这样个人账号和工作 / 学校账号就都能用。
5. **重定向 URI**先不用填（下一步单独加平台），点**注册**。

### 2. 身份验证：加“移动和桌面应用程序”平台

1. 注册好后进应用的**概述**页，左侧点**身份验证**。
2. 点**添加平台**。
3. 在弹出的“配置平台”里选**移动和桌面应用程序**。
4. 在底部的输入框里填 `http://localhost`，点**配置**。
   - 上面三个预置项（`https://login.microsoftonline.com/common/oauth2/nativeclient` 等）**不用勾**。
   - 只填裸 `http://localhost`（不带端口）：ZyDrive 运行时用随机端口，
     Azure 对 `http://localhost` 按回环惯例放行，任意端口都匹配。
   - 注意：这一步弹框里没有现成的 localhost 选项，要在底部输入框自己填。
5. 回到**身份验证**页，确认“移动和桌面应用程序”下出现了
   `http://localhost`。

### 3. API 权限：加 Microsoft Graph 委派权限

1. 左侧点 **API 权限**。
2. 点**添加权限** → 选 **Microsoft Graph**。
3. 选**委派的权限**。
4. 搜索并勾选以下两项：
   - `Files.ReadWrite.All`（读写用户的所有文件）
   - `offline_access`（拿 Refresh Token 用；没有它拿不到 Refresh Token）
5. 点**添加权限**。
6. 个人账号：**不用点“代表组织授予管理员同意”**，可以跳过（个人账号没有
   组织管理员同意这回事）；工作 / 学校账号按组织要求来。

### 4. 复制应用程序（客户端）ID

1. 左侧点**概述**。
2. 复制**应用程序（客户端）ID**——这就是 Client ID。
   - **不要创建“客户端密码”**：ZyDrive 官方应用是公开客户端（桌面应用），
     用 PKCE，不用 Secret。代码里也没有 Secret 字段。
3. 把客户端 ID 填回 ZyDrive 仓库文件 `internal/app/builtin-oauth-clients.json`
   的 `onedrive` 那一行（同时同步 `internal/app/builtin_oauth.go` 里的常量
   兜底值），重新构建。

### 5. 可选：品牌化里传应用图标

1. 左侧点**品牌化**。
2. 上传 ZyDrive 的绿云 logo（仓库 `frontend/public/logo.svg`，或由此导出
   的 PNG）当应用图标。
3. 这样用户授权时看到的不再是默认图标，而是 ZyDrive 的云朵。

## 状态与上限

- Azure 上一般不用单独“发布”：注册好、配好权限就能用。
- 首次授权时，用户可能看到“未经验证的应用”类提示，点继续即可（这是
  未验证发布者的正常提示，不是错误）。
- 个人账号注册的应用没有订阅费用；工作 / 学校账号注册可能受组织策略限制，
  被限制时换个人账号注册。

## 验证

1. 重新构建 ZyDrive，添加网盘选 OneDrive，第 2 步选“官方应用（一键授权）”，
   点“打开浏览器授权”。
2. 浏览器里登录 Microsoft 账号点允许，应自动跳回 ZyDrive、自动换到 Token，
   不用手动复制 code。
3. 如果浏览器没跳回：从地址栏复制 `code=` 后面的值，粘贴回“授权码”栏，
   点“换取 Token”。
4. 若授权页报 `AADSTS50011：回复 URL 不匹配`：回第 2 步检查“移动和桌面
   应用程序”下是否已添加 `http://localhost`。
5. 若授权后换 Token 失败、提示缺少 `offline_access`：回第 3 步检查 API
   权限里是否勾了 `offline_access`。
