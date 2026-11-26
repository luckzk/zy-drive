// SPDX-License-Identifier: MPL-2.0
package app

import (
	_ "embed"
	"encoding/json"
	"log"
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
//  Box and pCloud have NO builtin app, by design (not "not yet
//  registered"): their token endpoints require a Client Secret
//  at exchange time, so a shared Client ID alone could never
//  one-click — the user must register their own app and fill in
//  ID + Secret (the wizard's "自己的应用" is the only path, and
//  the one-click card shows "还没配置" with that explanation).
//  loadBuiltinClientsFrom accepts box/pcloud keys in the JSON
//  so the format stays uniform, but there is no builtin value
//  for them and there never will be one: even a filled-in ID
//  would still need a secret the shared flow cannot supply.
//
// The fallback still applies to any provider whose builtin ID
// is cleared or overridden away: its BuiltinOAuthClientID is
// "" and the wizard falls back to the self-built app flow
// (advanced fold) for that provider — never an error, never a
// blank page.
//
// ── Where the builtin IDs live ─────────────────────────────────
// The source of truth is builtin-oauth-clients.json, next to this
// file in the repo (plaintext — Client IDs are public identifiers,
// not secrets; the file header says so). It is embedded into the
// binary with go:embed, so the shipped app always carries it:
// a developer edits that one file and rebuilds, no code change.
// If the embedded file is missing or malformed, the constants
// below (same values) stand in, so a bad edit can never break
// startup. The full registration steps live in docs/oauth-apps/.
//
// A user's OWN app never lives here: it is stored separately in
// <configDir>/custom-oauth-clients.json (internal/app/
// custom_oauth.go), one entry per provider, and switching to it
// in the wizard never touches the builtin values — switching back
// to "官方应用" always finds them intact. The old
// oauth-clients.json override file (which DID overwrite the
// builtin per provider) is migrated away on startup, never
// applied: see MigrateLegacyOAuthClients.
//
// ── Shared-client risk, stated plainly ─────────────────────────
// A shared OAuth client is a single point of failure: if the
// provider rate-limits, flags, or suspends the ZyDrive app
// (rclone's shared Google client is being phased out through
// 2026 for exactly this reason), one-click authorization breaks
// for every user at once — while a self-built app keeps working,
// because its quota and standing are the user's own. That is why
// the self-built entry ("自己的应用") is permanent, is never
// removed, and is one click away from the one-click button.
// Users who hit limits or who prefer not to depend on the shared
// app should use their own app.

//go:embed builtin-oauth-clients.json
var builtinOAuthClientsJSON []byte

// builtinClientIDFallbacks are the same three Client IDs, as code
// constants. They only stand in when the embedded JSON cannot be
// read or parsed — the JSON file is the source of truth, these
// are the safety net, and both must be kept in sync.
var builtinClientIDFallbacks = map[domain.ProviderKind]string{
	domain.ProviderDropbox:  "ocx73kxhcpga24m", // Dropbox App key, app "ZyDrive-app", Full Dropbox
	domain.ProviderDrive:    "504630572830-9nkremocc6fsmbrg0lpk5n185ii4b447.apps.googleusercontent.com", // Google OAuth 客户端 ID, 应用类型“桌面应用”, app "ZyDrive-app"
	domain.ProviderOneDrive: "367e206e-fdfb-4372-8380-7f795ee42533", // Azure 应用程序（客户端）ID, app "ZyDrive-app", 移动和桌面应用程序, http://localhost
}

var (
	builtinMu      sync.RWMutex
	builtinClients = map[domain.ProviderKind]string{}
)

func init() {
	builtinClients = loadBuiltinClients()
}

// loadBuiltinClients parses the embedded JSON; on any failure it
// returns the code constants and logs why, so a bad edit to the
// JSON file is loud but never fatal.
func loadBuiltinClients() map[domain.ProviderKind]string {
	return loadBuiltinClientsFrom(builtinOAuthClientsJSON)
}

// loadBuiltinClientsFrom is the testable core: the JSON bytes are
// the source of truth, the code constants are the fallback.
func loadBuiltinClientsFrom(raw []byte) map[domain.ProviderKind]string {
	out := map[domain.ProviderKind]string{}
	for p, id := range builtinClientIDFallbacks {
		if strings.TrimSpace(id) != "" {
			out[p] = strings.TrimSpace(id)
		}
	}
	if len(raw) == 0 {
		return out
	}
	var file map[string]string
	if err := json.Unmarshal(raw, &file); err != nil {
		log.Printf("builtin-oauth-clients.json: malformed (%v), using code constants", err)
		return out
	}
	for key, id := range file {
		// Keys starting with "_" are comments/metadata, not providers.
		if strings.HasPrefix(key, "_") {
			continue
		}
		p := domain.ProviderKind(strings.TrimSpace(key))
		switch p {
		case domain.ProviderDropbox, domain.ProviderDrive, domain.ProviderOneDrive,
			domain.ProviderBox, domain.ProviderPCloud:
			// Box/pCloud are accepted here for format uniformity,
			// but the JSON carries no values for them (see the
			// file header: their exchanges need a Client Secret,
			// so a shared ID alone could never one-click). An
			// empty value clears the entry, like the other three.
			if strings.TrimSpace(id) != "" {
				out[p] = strings.TrimSpace(id)
			} else {
				delete(out, p)
			}
		default:
			// Unknown keys are ignored, never an error.
		}
	}
	return out
}

// BuiltinOAuthClientID returns the configured shared ZyDrive
// OAuth Client ID for a provider, or "" when the official app is
// not registered yet. The wizard treats "" as "offer the
// self-built flow only". Client IDs are public, so returning one
// to the frontend is safe; there is never a secret here. A user's
// own app never changes this value — it lives in the separate
// custom store (custom_oauth.go) and the wizard picks between
// the two explicitly.
func (a *App) BuiltinOAuthClientID(provider domain.ProviderKind) string {
	builtinMu.RLock()
	defer builtinMu.RUnlock()
	return builtinClients[provider]
}
