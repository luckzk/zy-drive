// SPDX-License-Identifier: MPL-2.0
package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// mountload.go — the startup mount load (v0.18.0).
//
// 2026-11-27 the desktop froze on start: LoadMounts ran synchronously
// before the window opened, and one MEGA mount's LoginWithKeys dialled
// g.api.mega.co.nz:443 direct (proxy.mode=direct) and hung forever —
// no window, no log, no way into Settings to fix the proxy. So the
// load is now bounded and asynchronous:
//   - LoadMountsAsync runs it in the background; the window opens
//     first and mounts appear as they finish (MountsLoading +
//     ListMounts, which returns the mounts loaded so far).
//   - Each mount gets 30s; the whole load gets 120s. A mount that
//     times out is listed with status "error" and a LoadError, never
//     dropped silently, and a late login cannot resurrect it.
//   - LoadMounts itself stays synchronous for the Agent, the CLI
//     and tests.

// mountLoadTimeoutPerMount / mountLoadTimeoutTotal bound the startup
// load. Vars, not consts, so tests can shrink them without waiting
// out the real 30s / 120s.
var (
	mountLoadTimeoutPerMount = 30 * time.Second
	mountLoadTimeoutTotal    = 120 * time.Second
)

// LoadMounts rebuilds every persisted mount after a restart: the
// mount row gives metadata + non-secret config, the secrets table
// gives the encrypted credentials (decrypted in memory only). A
// mount whose provider cannot be rebuilt (bad/expired credentials,
// missing master key) is kept in the list with status "error" and no
// provider, so the user still sees it and can remove or re-add it —
// it is never silently dropped. Call once at startup, after the
// master key store is attached.
func (a *App) LoadMounts() error {
	if a.DB == nil {
		return nil
	}
	persisted, err := a.DB.ListMounts()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(mountLoadTimeoutTotal)
	for _, pm := range persisted {
		if time.Now().After(deadline) {
			// Total budget spent: the mounts still unloaded are
			// not retried this start — they are listed failed
			// with a reason, and the user retries them from
			// Settings (TestMount) once the network is fixed.
			// Copy: no build goroutine was started for this
			// mount, but the row must never be mutated in
			// place — the *domain.Mount from ListMounts is
			// shared with nobody, and keeping that invariant
			// is what makes the timeout path below safe.
			m := *pm.Mount
			m.Status = domain.MountError
			m.LoadError = fmt.Sprintf("加载超时：总限时 %s 已到，还没轮到这个网盘，请检查网络或代理后点“重试”", mountLoadTimeoutTotal)
			log.Printf("mounts: mount=%s provider=%s: skipped, total load budget exhausted", m.ID, m.Provider)
			a.setHandle(m.ID, MountHandle{Mount: &m})
			continue
		}
		timeout := mountLoadTimeoutPerMount
		if rest := time.Until(deadline); rest < timeout {
			timeout = rest
		}
		a.setHandle(pm.Mount.ID, a.loadPersistedMountWithTimeout(pm, timeout))
	}
	return nil
}

// LoadMountsAsync runs LoadMounts in the background: the desktop
// starts it with `go` before the window opens, so a hung mount can
// no longer wedge startup. A second concurrent call is a no-op —
// the load is already running.
func (a *App) LoadMountsAsync() {
	a.mountsMu.Lock()
	if a.mountsLoading {
		a.mountsMu.Unlock()
		return
	}
	a.mountsLoading = true
	a.mountsMu.Unlock()
	defer func() {
		a.mountsMu.Lock()
		a.mountsLoading = false
		a.mountsMu.Unlock()
	}()
	if err := a.LoadMounts(); err != nil {
		log.Printf("mounts: async load: %v", err)
	}
}

// MountsLoading reports whether a background LoadMountsAsync is
// still running. The frontend polls it while the sidebar fills in.
func (a *App) MountsLoading() bool {
	a.mountsMu.RLock()
	defer a.mountsMu.RUnlock()
	return a.mountsLoading
}

// loadPersistedMountWithTimeout builds one persisted mount, giving
// up after timeout. The build runs in its own goroutine because a
// provider login (MEGA LoginWithKeys) does not take a context and
// cannot be cancelled: on timeout the goroutine is abandoned — its
// result channel is buffered and never read, and buildPersistedMount
// never touches a.mounts, so a late login cannot overwrite the
// failure record or resurrect the mount.
func (a *App) loadPersistedMountWithTimeout(pm cache.PersistedMount, timeout time.Duration) MountHandle {
	ch := make(chan MountHandle, 1)
	go func() { ch <- a.buildPersistedMount(pm) }()
	select {
	case h := <-ch:
		return h
	case <-time.After(timeout):
		// Copy, never mutate pm.Mount in place: the abandoned
		// build goroutine still owns that struct (its heal path
		// reads it in SaveMount, and it sets Status itself) —
		// writing it here while the build runs is a data race
		// (caught by -race 2026-11-27). The copy is what the
		// list shows; the late build result is discarded.
		m := *pm.Mount
		m.Status = domain.MountError
		m.LoadError = fmt.Sprintf("加载超时（%s 内无响应）：可能是网络挂起，请检查代理设置后点“重试”", timeout)
		log.Printf("mounts: mount=%s provider=%s: load timed out after %s", m.ID, m.Provider, timeout)
		return MountHandle{Mount: &m}
	}
}

// buildPersistedMount is the old LoadMounts loop body, extracted:
// rebuild one persisted mount from its row + secrets and classify
// the outcome. It returns the handle instead of writing the map —
// the caller (LoadMounts, or the timeout wrapper) owns the write,
// so an abandoned timed-out build can never touch live state. It
// never returns a nil Mount.
func (a *App) buildPersistedMount(pm cache.PersistedMount) MountHandle {
	// Work on a copy: the timeout wrapper may abandon this
	// goroutine and report the mount failed from its own copy of
	// the same row — the two goroutines must never share one
	// mutable *domain.Mount, or the failure write races the
	// build's heal/save reads (caught by -race 2026-11-27).
	m := *pm.Mount
	fail := func(status domain.MountStatus, loadErr string) MountHandle {
		m.Status = status
		m.LoadError = loadErr
		return MountHandle{Mount: &m}
	}
	// Per-mount proxy (v0.18.0): read from the stored config map
	// ("proxy_ref") and resolved to the proxy the driver must
	// use. "" follows the global setting, so old rows without the
	// key behave exactly as before.
	m.ProxyRef = pm.Config["proxy_ref"]
	// Display-only endpoint host, recomputed from the stored
	// non-secret config on every load (older rows never had
	// the field; nothing is persisted for it here).
	m.EndpointHost = endpointHost(pm.Config["endpoint"])
	// Workspace label (Drime only): recomputed from the stored
	// non-secret config on every load, so mounts saved before
	// this field existed still show their workspace.
	m.WorkspaceLabel = workspaceLabelFromConfig(m.Provider, pm.Config)
	// Self-heal (2026-11-25, S3 root fix): mounts saved before
	// the fix carry RootRef "bucket" or "bucket/prefix", which
	// the browse/sync/upload code treats as a key prefix — the
	// bucket root then looks empty and uploads land under
	// bucket/bucket/. Recompute from the stored config
	// (bucket/prefix are untouched by the bug) and write the
	// corrected row back, so the user's existing R2 mount just
	// works on the next start with no delete-and-re-add. A row
	// whose own prefix repeats the bucket name cannot be healed
	// this way — that needs the Settings "修改桶 / 前缀" entry.
	if wantRef, wantLabel, ok := healedRoot(m.Provider, pm.Config); ok && (m.RootRef != wantRef || m.RootLabel != wantLabel) {
		m.RootRef = wantRef
		m.RootLabel = wantLabel
		m.UpdatedAt = time.Now().UTC()
		// Best effort: a failed write-back only means the heal
		// retries on the next start; the in-memory mount is
		// already corrected.
		_ = a.DB.SaveMount(&m, pm.Config)
	}
	cfg := storage.Config{
		Endpoint: pm.Config["endpoint"],
		Region:   pm.Config["region"],
		Extra:    pm.Config,
	}
	cfg.Proxy = a.resolveMountProxyConfig(m.ProxyRef)
	a.mountRuntime(&cfg, m.ID)
	if a.secrets != nil {
		if ct, err := a.DB.LoadSecret(m.AccountID); err == nil {
			plain, err := a.secrets.Decrypt(ct)
			if err != nil {
				return fail(domain.MountError, fmt.Sprintf("保存的凭证解不开：%v", err))
			}
			var creds mountCredentials
			if err := json.Unmarshal(plain, &creds); err != nil {
				return fail(domain.MountError, "保存的凭证损坏了，读不出来，请重新授权")
			}
			cfg.AccessKey = creds.AccessKey
			cfg.SecretKey = creds.SecretKey
			cfg.Token = creds.AccessToken
			cfg.Extra["refresh_token"] = creds.RefreshToken
			cfg.Extra["client_secret"] = creds.ClientSecret
			// MEGA: the stored session (never the password).
			// A mount saved before v0.9.1 has no session and
			// the driver then refuses to build — the user
			// re-authorizes it once from Settings.
			if creds.MegaSession != "" {
				cfg.Extra["mega_session_id"] = creds.MegaSession
			}
			if creds.MegaMasterKey != "" {
				cfg.Extra["mega_master_key"] = creds.MegaMasterKey
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fail(domain.MountError, fmt.Sprintf("读保存的凭证失败：%v", err))
		}
		// No secret row: a local mount (no credentials). Fine.
	} else if m.Provider != domain.ProviderLocal {
		return fail(domain.MountError, "凭证存储还没初始化（主密钥缺失），请重启应用再试")
	}
	// Self-heal (2026-11-27, builtin client_id fix): mounts
	// authorized through the one-click builtin flow were saved
	// without a client_id, so the first token refresh after
	// the access token expired failed with invalid_client.
	// Fill the builtin ID here so existing mounts heal
	// without re-authorisation; the corrected row is written
	// back best-effort, like the S3 root heal above. A stored
	// custom Client ID is never overwritten.
	if a.fillBuiltinClientID(m.Provider, cfg.Extra) {
		_ = a.DB.SaveMount(&m, pm.Config)
	}
	p, err := a.Registry.New(m.Provider, cfg)
	if err != nil {
		// Log the real reason: the UI only shows a status,
		// so without this line the cause (e.g. an expired
		// MEGA session) is invisible anywhere. Provider
		// errors never contain credential values, so this
		// is safe to log.
		log.Printf("mounts: mount=%s provider=%s: %v", m.ID, m.Provider, err)
		if errors.Is(err, storage.ErrSessionExpired) {
			// The stored session is dead; only a fresh
			// login revives the mount. The UI already
			// renders this status as "授权过期了，重新授权
			// 后就能继续浏览。"
			return fail(domain.MountAuthExpired, err.Error())
		}
		return fail(domain.MountError, err.Error())
	}
	m.Caps = p.Caps()
	if m.Status != domain.MountOK {
		m.Status = domain.MountOK
	}
	m.LoadError = ""
	return MountHandle{Mount: &m, Provider: p}
}
