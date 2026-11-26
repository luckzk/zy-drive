// SPDX-License-Identifier: MPL-2.0
package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/luckzk/zy-drive/internal/domain"
)

// custom_oauth.go — the user's OWN OAuth apps, one per provider,
// stored strictly separately from the builtin ZyDrive official
// apps (builtin_oauth.go). The separation is the point:
//
//   - The builtin IDs are read-only defaults shipped in the repo.
//     Nothing the user does in the wizard ever writes to them.
//   - A custom app lives in <configDir>/custom-oauth-clients.json
//     (0600, next to master.key). Switching the wizard to
//     "自己的应用" reads this file; switching back to "官方应用"
//     reads the builtin map — the builtin is always there to come
//     back to, even after years of using a custom app.
//
// File format (one entry per provider; only OAuth providers):
//
//	{
//	  "_comment": "...",
//	  "dropbox":  {"clientId": "<App key>", "clientSecretEnc": "<base64 AES-GCM ciphertext, or omitted>"},
//	  "drive":    {"clientId": "<OAuth 客户端 ID>", "clientSecretEnc": "..."},
//	  "onedrive": {"clientId": "<应用程序（客户端）ID>", "clientSecretEnc": "..."}
//	}
//
// Client ID is a public identifier and is stored in plaintext.
// Client Secret, when the user fills one in (Dropbox / Drive;
// OneDrive desktop apps have none), is encrypted with the master
// key (AES-256-GCM, same SecretsStore as mount credentials) and
// base64'd — it is never written to disk in plaintext, never
// logged, and only decrypted into memory when the wizard needs
// it for an exchange. GetCustomOAuthApp returns the secret to
// the user's own wizard (in memory only) for exactly that one
// purpose; the frontend never persists it anywhere else.

// CustomOAuthApp is one provider's user-owned OAuth app. The
// Secret travels in memory only, from this store to the wizard
// form to the token exchange; it is never stored in plaintext.
type CustomOAuthApp struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret,omitempty"` // in-memory only
	HasSecret    bool   `json:"hasSecret"`
}

// customOAuthAppFile is one entry as stored on disk.
type customOAuthAppFile struct {
	ClientID        string `json:"clientId"`
	ClientSecretEnc string `json:"clientSecretEnc,omitempty"` // base64 AES-GCM ciphertext
}

// customOAuthFile is the whole custom store file.
type customOAuthFile struct {
	Comment  string              `json:"_comment,omitempty"`
	Dropbox  *customOAuthAppFile `json:"dropbox,omitempty"`
	Drive    *customOAuthAppFile `json:"drive,omitempty"`
	OneDrive *customOAuthAppFile `json:"onedrive,omitempty"`
}

func (f *customOAuthFile) entry(p domain.ProviderKind) *customOAuthAppFile {
	switch p {
	case domain.ProviderDropbox:
		return f.Dropbox
	case domain.ProviderDrive:
		return f.Drive
	case domain.ProviderOneDrive:
		return f.OneDrive
	default:
		return nil
	}
}

func (f *customOAuthFile) setEntry(p domain.ProviderKind, e *customOAuthAppFile) {
	switch p {
	case domain.ProviderDropbox:
		f.Dropbox = e
	case domain.ProviderDrive:
		f.Drive = e
	case domain.ProviderOneDrive:
		f.OneDrive = e
	}
}

const customOAuthFileComment = "用户自己的 OAuth 应用（每网盘一个），与 ZyDrive 内置官方应用分开存放。Client ID 是公开标识；Client Secret 如有，用主密钥加密后存在 clientSecretEnc 里（base64），绝不明文存放。删掉某一行就是删掉那个网盘自己的应用，内置官方应用不受影响。"

// CustomOAuthProviders are the providers a custom app can exist for.
var CustomOAuthProviders = []domain.ProviderKind{domain.ProviderDropbox, domain.ProviderDrive, domain.ProviderOneDrive}

func isCustomOAuthProvider(p domain.ProviderKind) bool {
	for _, c := range CustomOAuthProviders {
		if c == p {
			return true
		}
	}
	return false
}

var (
	customOAuthMu   sync.RWMutex
	customOAuthApps = map[domain.ProviderKind]CustomOAuthApp{}
	customOAuthPath string
)

// GetCustomOAuthApp returns the user's own saved app for a
// provider, or nil when none is saved. The secret is returned
// only to the user's own wizard (in memory, for the exchange);
// it is never logged and never written to disk unencrypted.
func (a *App) GetCustomOAuthApp(provider domain.ProviderKind) *CustomOAuthApp {
	if !isCustomOAuthProvider(provider) {
		return nil
	}
	customOAuthMu.RLock()
	defer customOAuthMu.RUnlock()
	if app, ok := customOAuthApps[provider]; ok {
		c := app
		return &c
	}
	return nil
}

// LoadCustomOAuthClients reads <configDir>/custom-oauth-clients.json
// into memory. A missing file means "no custom apps yet" (not an
// error). A malformed file is an error — a half-loaded custom
// store must never silently authorize with the wrong app. An
// entry whose secret cannot be decrypted (master key changed or
// file tampered) is also an error, for the same reason. Call
// after SetSecrets: decryption needs the master key.
func (a *App) LoadCustomOAuthClients(path string) error {
	customOAuthMu.Lock()
	defer customOAuthMu.Unlock()
	customOAuthPath = path
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("app: 读取自己的应用配置失败：%w", err)
	}
	var file customOAuthFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("app: 自己的应用配置格式不对：%w", err)
	}
	loaded := map[domain.ProviderKind]CustomOAuthApp{}
	for _, p := range CustomOAuthProviders {
		e := file.entry(p)
		if e == nil || strings.TrimSpace(e.ClientID) == "" {
			continue
		}
		app := CustomOAuthApp{ClientID: strings.TrimSpace(e.ClientID)}
		if strings.TrimSpace(e.ClientSecretEnc) != "" {
			if a.secrets == nil {
				return fmt.Errorf("app: 自己的应用里有加密的 Secret，但主密钥还没加载")
			}
			ct, err := base64.StdEncoding.DecodeString(strings.TrimSpace(e.ClientSecretEnc))
			if err != nil {
				return fmt.Errorf("app: 自己的应用 Secret 解不开（base64）：%w", err)
			}
			plain, err := a.secrets.Decrypt(ct)
			if err != nil {
				return fmt.Errorf("app: 自己的应用 Secret 解不开（主密钥不对或文件被改过）：%w", err)
			}
			app.ClientSecret = string(plain)
			app.HasSecret = true
		}
		loaded[p] = app
	}
	customOAuthApps = loaded
	return nil
}

// SaveCustomOAuthApp stores (or replaces) the user's own app for a
// provider. An empty Client ID deletes the entry instead (same as
// DeleteCustomOAuthApp). The secret, when non-empty, is encrypted
// with the master key before anything touches disk; the file is
// written atomically (temp + rename) with 0600 permissions.
// Saving a custom app never touches the builtin IDs.
func (a *App) SaveCustomOAuthApp(provider domain.ProviderKind, clientID, clientSecret string) error {
	if !isCustomOAuthProvider(provider) {
		return fmt.Errorf("app: %s 不支持保存自己的 OAuth 应用", provider)
	}
	clientID = strings.TrimSpace(clientID)
	customOAuthMu.Lock()
	defer customOAuthMu.Unlock()
	if customOAuthPath == "" {
		return fmt.Errorf("app: 自己的应用存放位置还没配置，请重启应用再试")
	}
	// Re-read the file first so a save never drops an entry written
	// by another process (or an earlier load) that memory missed.
	file, err := a.readCustomOAuthFileLocked()
	if err != nil {
		return err
	}
	if clientID == "" {
		file.setEntry(provider, nil)
	} else {
		e := &customOAuthAppFile{ClientID: clientID}
		if strings.TrimSpace(clientSecret) != "" {
			if a.secrets == nil {
				return fmt.Errorf("app: 保存 Secret 需要主密钥，请重启应用再试")
			}
			ct, err := a.secrets.Encrypt([]byte(clientSecret))
			if err != nil {
				return fmt.Errorf("app: 加密 Secret 失败：%w", err)
			}
			e.ClientSecretEnc = base64.StdEncoding.EncodeToString(ct)
		}
		file.setEntry(provider, e)
	}
	if err := writeCustomOAuthFileLocked(customOAuthPath, file); err != nil {
		return err
	}
	// Mirror the write into memory.
	if clientID == "" {
		delete(customOAuthApps, provider)
	} else {
		customOAuthApps[provider] = CustomOAuthApp{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			HasSecret:    strings.TrimSpace(clientSecret) != "",
		}
	}
	return nil
}

// DeleteCustomOAuthApp removes the user's own app for a provider.
// The builtin official app is untouched — the wizard falls back
// to it immediately.
func (a *App) DeleteCustomOAuthApp(provider domain.ProviderKind) error {
	return a.SaveCustomOAuthApp(provider, "", "")
}

// readCustomOAuthFileLocked reads the on-disk custom store (or an
// empty one when the file does not exist yet). Caller holds the
// write lock.
func (a *App) readCustomOAuthFileLocked() (*customOAuthFile, error) {
	file := &customOAuthFile{Comment: customOAuthFileComment}
	if customOAuthPath == "" {
		return file, nil
	}
	raw, err := os.ReadFile(customOAuthPath)
	if err != nil {
		if os.IsNotExist(err) {
			return file, nil
		}
		return nil, fmt.Errorf("app: 读取自己的应用配置失败：%w", err)
	}
	if err := json.Unmarshal(raw, file); err != nil {
		return nil, fmt.Errorf("app: 自己的应用配置格式不对：%w", err)
	}
	if file.Comment == "" {
		file.Comment = customOAuthFileComment
	}
	return file, nil
}

// writeCustomOAuthFileLocked writes the custom store atomically
// (temp file + rename, 0600). Caller holds the write lock.
func writeCustomOAuthFileLocked(path string, file *customOAuthFile) error {
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("app: 保存自己的应用失败：%w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("app: 保存自己的应用失败：%w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("app: 保存自己的应用失败：%w", err)
	}
	return nil
}

// MigrateLegacyOAuthClients moves the old oauth-clients.json
// override file into the custom store. The old file's semantics
// were "override the builtin per provider" — the exact thing the
// separate custom store exists to prevent — so it is never
// applied as an override again. Each non-empty value becomes that
// provider's custom app (Client ID only, no secret — the old file
// never held one); a provider that already has a custom app keeps
// its existing entry, the legacy value does not overwrite it.
// Afterwards the legacy file is renamed to
// oauth-clients.json.migrated, so the migration runs exactly once
// and the user can see what happened. A missing legacy file is a
// no-op. Returns how many providers were migrated.
func (a *App) MigrateLegacyOAuthClients(legacyPath, customPath string) (int, error) {
	raw, err := os.ReadFile(legacyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("app: 读取旧的 oauth-clients.json 失败：%w", err)
	}
	var legacy map[string]string
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return 0, fmt.Errorf("app: 旧的 oauth-clients.json 格式不对：%w", err)
	}
	migrated := 0
	for key, id := range legacy {
		p := domain.ProviderKind(strings.TrimSpace(key))
		if !isCustomOAuthProvider(p) || strings.TrimSpace(id) == "" {
			continue
		}
		customOAuthMu.RLock()
		_, exists := customOAuthApps[p]
		customOAuthMu.RUnlock()
		if exists {
			// The custom store already has this provider: the
			// newer entry wins, the legacy value is dropped —
			// but the rename below still retires the file.
			continue
		}
		if err := a.SaveCustomOAuthApp(p, strings.TrimSpace(id), ""); err != nil {
			return migrated, err
		}
		migrated++
	}
	// Retire the legacy file, whatever it held: it must never be
	// applied as a builtin override again.
	if err := os.Rename(legacyPath, legacyPath+".migrated"); err != nil {
		// The values are already safe in the custom store; a
		// failed rename must not fail startup, but it must be
		// loud — otherwise the file gets re-read next boot.
		return migrated, fmt.Errorf("app: 旧的 oauth-clients.json 已迁入自己的应用，但重命名失败（下次启动会再迁一次）：%w", err)
	}
	return migrated, nil
}
