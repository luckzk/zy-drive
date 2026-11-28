// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"fmt"
	"time"

	"github.com/luckzk/zy-drive/internal/mount"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// mountbind.go — the app layer's wiring for the FUSE volume
// (v0.19.0, P0). The MountService lives in internal/mount and
// never imports this package; this file hands it closures
// (provider lookup, mount list, switch persistence, limiter,
// cache cap) and exposes its state through App methods that
// bind.go binds to the frontend.
//
// Mounting is free (user decision, 2026-11-27): no Pro gate
// anywhere on this path.

// mountService returns the app's MountService, building it
// lazily on first use.
func (a *App) mountService() *mount.Service {
	a.mountSvcMu.Lock()
	defer a.mountSvcMu.Unlock()
	if a.mountSvc != nil {
		return a.mountSvc
	}
	a.mountSvc = mount.New(mount.Deps{
		DB:          a.DB,
		ProviderFor: a.ProviderFor,
		ListMounts:  a.ListMounts,
		SaveEnabled: a.saveMountEnabled,
		Limiter: func() *transfer.Limiter {
			if a.transferLimiter == nil {
				a.ApplyTransferSettings()
			}
			return a.transferLimiter
		},
		CacheLimitGB: func() int {
			s, err := a.GetSettings()
			if err != nil {
				return mount.DefaultCacheLimitGB
			}
			return s.MountCacheLimitGB
		},
	})
	return a.mountSvc
}

// InitMountService reconciles the mount cache with disk and
// starts the write-back upload worker. Called once at desktop
// startup; a failure is logged by the caller, never fatal.
func (a *App) InitMountService() error {
	return a.mountService().Start()
}

// saveMountEnabled persists one mount's switch in the config
// map ("mount_enabled" = "1"/"0", the ProxyRef / "proxy_ref"
// precedent — no new mounts-table column, no new migration)
// and updates the in-memory Mount copy-on-write.
func (a *App) saveMountEnabled(mountID string, enabled bool) error {
	h, ok := a.Mount(mountID)
	if !ok {
		return fmt.Errorf("app: unknown mount %q", mountID)
	}
	if a.DB == nil {
		return fmt.Errorf("app: 本地数据库还没初始化，开关保存不了")
	}
	cfgMap, err := a.persistedConfig(mountID)
	if err != nil {
		return err
	}
	newCfg := map[string]string{}
	for k, v := range cfgMap {
		newCfg[k] = v
	}
	if enabled {
		newCfg["mount_enabled"] = "1"
	} else {
		newCfg["mount_enabled"] = "0"
	}
	m := *h.Mount
	m.MountEnabled = enabled
	m.UpdatedAt = time.Now().UTC()
	a.setHandle(mountID, MountHandle{Mount: &m, Provider: h.Provider})
	return a.DB.SaveMount(&m, newCfg)
}

// GetMountStatus snapshots the volume state, the driver state
// and every mountable mount (frontend polls this).
func (a *App) GetMountStatus() mount.VolumeStatus {
	return a.mountService().Status()
}

// SetMountEnabled flips one mount's FUSE switch.
func (a *App) SetMountEnabled(ctx context.Context, mountID string, enabled bool) error {
	return a.mountService().SetEnabled(ctx, mountID, enabled)
}

// GetMountCacheInfo reports the mount cache (size, count,
// dirty, limit) for Settings → 挂载.
func (a *App) GetMountCacheInfo() (mount.CacheInfo, error) {
	return a.mountService().CacheInfo()
}

// ClearMountCache deletes cached files that are neither dirty
// nor pinned and reports the freed bytes.
func (a *App) ClearMountCache() (mount.CacheCleared, error) {
	return a.mountService().ClearCache()
}

// RetryMount re-enables a mount ("关→开": same validation and
// mount path as a switch tap).
func (a *App) RetryMount(ctx context.Context, mountID string) error {
	return a.mountService().Retry(ctx, mountID)
}

// GetMountExitState reports dirty files/bytes + open files for
// the exit dialog.
func (a *App) GetMountExitState() mount.ExitState {
	return a.mountService().ExitState()
}

// FlushMountDirty uploads dirty files within 30s (exit path).
func (a *App) FlushMountDirty(ctx context.Context) (mount.FlushResult, error) {
	return a.mountService().FlushDirty(30 * time.Second), nil
}

// RestoreMounts re-applies the persisted switches after the
// startup mount load finishes. Called from a goroutine in
// main once MountsLoading() is false.
func (a *App) RestoreMounts() {
	a.mountService().RestoreEnabled(context.Background())
}

// ShutdownMounts flushes dirty files (30s best-effort) and
// unmounts the volume. Called after the window loop returns.
func (a *App) ShutdownMounts() error {
	return a.mountService().Shutdown(context.Background())
}

// trimMountCacheBestEffort trims the mount cache after a
// Settings save shrinks its cap (called from SaveSettings;
// a failure is swallowed — the next cache use trims again).
func (a *App) trimMountCacheBestEffort() {
	a.mountService().TrimToLimit()
}
