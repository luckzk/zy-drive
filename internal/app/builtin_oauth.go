package app

import (
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/luckzk/zy-drive/internal/domain"
)

// builtin_oauth.go — the built-in shared ZyDrive OAuth apps (B,
// one-click authorization). All three registrations are DONE
// (2026-11-25, user registered them and pasted the Client IDs
// in chat — Client IDs are public identifiers, not secrets,
// so they ship in this repo as defaults):
//
//  1. Dropbox — DONE. App Console (dropbox.com/developers/apps),
//     app "ZyDrive-app", Scoped access, Full Dropbox, "Allow
//     public clients" (允许公共客户端) ON, Redirect URI
//     `http://localhost` added. Permissions:
//     files.metadata.read, files.metadata.write,
//     files.content.read, files.content.write. The App key IS
//     the Client ID. No App secret is used (PKCE replaces it).
//
//  2. Google Drive — DONE. Google Cloud Console
//     (console.cloud.google.com), app "ZyDrive-app", OAuth 客户端
//     ID, 应用类型“桌面应用”, Authorized redirect URI
//     `http://localhost`. The OAuth 同意屏幕 needs the app
//     published (or the user's own account added as a test user).
//     The Client ID is the builtin value; the Client Secret is
//     NOT used (PKCE replaces it). Scope requested:
//     https://www.googleapis.com/auth/drive.
//
//  3. OneDrive — DONE. Azure portal (portal.azure.com →
//     Microsoft Entra ID → 应用注册 → 新注册): name
//     "ZyDrive-app"; "受支持的帐户类型" → "任何组织目录中的
//     帐户和个人 Microsoft 帐户" → "身份验证" → 添加平台 →
//     "移动和桌面应用程序" → 重定向 URI `http://localhost` →
//     "API 权限" → 添加权限 → Microsoft Graph → 委派的权限 →
//     Files.ReadWrite.All + offline_access. The "应用程序（客户
//     端）ID" on the 概述 page is the builtin value. No secret
//     is created (public client + PKCE).
//
// The fallback still applies to any provider whose builtin ID
// is cleared or overridden away: its BuiltinOAuthClientID is
// "" and the wizard falls back to the self-built app flow
// (advanced fold) for that provider — never an error, never a
// blank page.
//
// ── How a Client ID takes effect ───────────────────────────────
// The constants below are the defaults (a developer edit, then
// rebuild). To override one without rebuilding, drop a JSON file
// named `oauth-clients.json` next to master.key in the config dir
// (~/.config/zy-drive/oauth-clients.json):
//   {"dropbox": "<App key>", "drive": "<Client ID>", "onedrive": "<应用程序（客户端）ID>"}
// Keys are provider kinds; a non-empty value overrides the
// builtin default for that provider, an absent/empty value keeps
// the default. The file is read once at desktop startup
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

// builtinClientIDs are the ZyDrive official app Client IDs
// shipped as defaults. All three providers are registered
// (2026-11-25, app "ZyDrive-app" in all three consoles).
// Leave a provider "" and the wizard falls back to the
// self-built flow. oauth-clients.json can override any of these
// per provider (non-empty values only); it never has to repeat
// them.
var builtinClientIDs = map[domain.ProviderKind]string{
	domain.ProviderDropbox:  "ocx73kxhcpga24m", // Dropbox App key, app "ZyDrive-app", Full Dropbox
	domain.ProviderDrive:    "504630572830-9nkremocc6fsmbrg0lpk5n185ii4b447.apps.googleusercontent.com", // Google OAuth 客户端 ID, 应用类型“桌面应用”, app "ZyDrive-app"
	domain.ProviderOneDrive: "367e206e-fdfb-4372-8380-7f795ee42533", // Azure 应用程序（客户端）ID, app "ZyDrive-app", 移动和桌面应用程序, http://localhost
}

var (
	builtinMu      sync.RWMutex
	builtinClients = map[domain.ProviderKind]string{}
)

func init() {
	for p, id := range builtinClientIDs {
		if strings.TrimSpace(id) != "" {
			builtinClients[p] = strings.TrimSpace(id)
		}
	}
}

// LoadBuiltinOAuthClients applies an oauth-clients.json file (the
// no-rebuild override path). A non-empty value overrides the
// builtin default for that provider; an absent/empty value keeps
// the default. Missing file: silently nothing (the defaults
// stand). Malformed file: the error is returned and the caller
// logs it — a bad file must never stop the app from starting,
// and it must never silently enable a wrong client.
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
