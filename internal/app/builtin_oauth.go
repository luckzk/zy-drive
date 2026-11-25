package app

import (
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/luckzk/zy-drive/internal/domain"
)

// builtin_oauth.go — the built-in shared ZyDrive OAuth apps (B,
// one-click authorization). The mechanism is code-complete; the
// registrations themselves are NOT done yet — an agent cannot walk
// into the Dropbox App Console / Google Cloud Console / Azure
// portal, create the apps and pass their reviews. Until the user
// registers them, every BuiltinOAuthClientID below is "" and the
// wizard falls back to the self-built app flow (advanced fold) —
// never an error, never a blank page.
//
// ── What the user must register (one app per provider) ──────────
// All three are PUBLIC clients (no secret is ever shipped or sent):
// the token exchange proves possession with PKCE (S256) only.
//
//  1. Dropbox — Dropbox App Console (dropbox.com/developers/apps):
//     Create app → Scoped access → Full Dropbox (or App folder) →
//     Permissions: files.metadata.read, files.metadata.write,
//     files.content.read, files.content.write → Settings →
//     Redirect URIs: add `http://localhost` (bare loopback; the
//     runtime port is random per authorization). The App key IS
//     the Client ID. No App secret is used (PKCE replaces it).
//
//  2. Google Drive — Google Cloud Console (console.cloud.google.com):
//     New project → "API 和服务" → enable Google Drive API →
//     "凭据" → 创建凭据 → OAuth 客户端 ID → 应用类型“桌面应用”
//     → Authorized redirect URIs: add `http://localhost`. The
//     OAuth 同意屏幕 needs the app published (or the user's own
//     account added as a test user). The Client ID is the builtin
//     value; the Client Secret is NOT used (PKCE replaces it).
//     Scope requested: https://www.googleapis.com/auth/drive.
//
//  3. OneDrive — Azure portal (portal.azure.com → Microsoft Entra
//     ID → 应用注册 → 新注册): name e.g. "ZyDrive"; "受支持的帐户
//     类型" → "任何组织目录中的帐户和个人 Microsoft 帐户" →
//     "身份验证" → 添加平台 → "移动和桌面应用程序" → 重定向 URI
//     `http://localhost` → "API 权限" → 添加权限 → Microsoft
//     Graph → 委派的权限 → Files.ReadWrite.All + offline_access.
//     The "应用程序（客户端）ID" on the 概述 page is the builtin
//     value. No secret is created (public client + PKCE).
//
// ── How a registered Client ID takes effect ─────────────────────
// Fill the constants below (a developer edit, then rebuild), OR
// — no rebuild needed — drop a JSON file named
// `oauth-clients.json` next to master.key in the config dir
// (~/.config/zy-drive/oauth-clients.json):
//   {"dropbox": "<App key>", "drive": "<Client ID>", "onedrive": "<应用程序（客户端）ID>"}
// Keys are provider kinds; an empty/absent value means "not
// configured". The file is read once at desktop startup
// (cmd/desktop/main.go); unknown keys are ignored. Client IDs are
// public identifiers, not secrets — they appear in the authorize
// URL in the browser anyway — but the file still lives in the
// config dir (0700), never in the repo.
//
// ── Shared-client risk, stated plainly ─────────────────────────
// A shared OAuth client is a single point of failure: if the
// provider rate-limits, flags, or suspends the ZyDrive app
// (rclone's shared Google client is being phased out through
// 2026 for exactly this reason), one-click authorization breaks
// for every user at once — while a self-built app keeps working,
// because its quota and standing are the user's own. That is why
// the self-built entry ("高级：用自己的应用") is permanent, is
// never removed, and is one click away from the one-click button.
// Users who hit limits or who prefer not to depend on the shared
// app should use their own app.

// builtinClientIDPlaceholders are the ZyDrive official app Client
// IDs. Empty until the three registrations above are done — fill
// them in (or use oauth-clients.json) and one-click authorization
// lights up for that provider; leave them empty and the wizard
// falls back to the self-built flow.
var builtinClientIDPlaceholders = map[domain.ProviderKind]string{
	domain.ProviderDropbox:  "", // TODO: Dropbox App key, from the App Console (see above)
	domain.ProviderDrive:    "", // TODO: Google OAuth 客户端 ID, 应用类型“桌面应用”
	domain.ProviderOneDrive: "", // TODO: Azure 应用程序（客户端）ID
}

var (
	builtinMu      sync.RWMutex
	builtinClients = map[domain.ProviderKind]string{}
)

func init() {
	for p, id := range builtinClientIDPlaceholders {
		if strings.TrimSpace(id) != "" {
			builtinClients[p] = strings.TrimSpace(id)
		}
	}
}

// LoadBuiltinOAuthClients applies an oauth-clients.json file (the
// no-rebuild path). Missing file: silently nothing (the
// placeholders stand). Malformed file: the error is returned and
// the caller logs it — a bad file must never stop the app from
// starting, and it must never silently enable a wrong client.
func LoadBuiltinOAuthClients(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var file map[string]string
	if err := json.Unmarshal(raw, &file); err != nil {
		return err
	}
	builtinMu.Lock()
	defer builtinMu.Unlock()
	for key, id := range file {
		p := domain.ProviderKind(strings.TrimSpace(key))
		switch p {
		case domain.ProviderDropbox, domain.ProviderDrive, domain.ProviderOneDrive:
			if strings.TrimSpace(id) != "" {
				builtinClients[p] = strings.TrimSpace(id)
			}
		}
	}
	return nil
}

// BuiltinOAuthClientID returns the configured shared ZyDrive
// OAuth Client ID for a provider, or "" when the official app is
// not registered yet. The wizard treats "" as "offer the
// self-built flow only". Client IDs are public, so returning one
// to the frontend is safe; there is never a secret here.
func (a *App) BuiltinOAuthClientID(provider domain.ProviderKind) string {
	builtinMu.RLock()
	defer builtinMu.RUnlock()
	return builtinClients[provider]
}
