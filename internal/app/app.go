// SPDX-License-Identifier: MPL-2.0
// Package app is the application layer: the only business-orchestration
// entry point. The UI (frontend via mygo.Bind), the CLI and the Agent
// all call into App. App only talks to the storage.Provider interface,
// the sync/transfer engines and the cache — it never imports a concrete
// driver package and never contains a vendor name.
package app

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/agentapi"
	"github.com/luckzk/zy-drive/internal/auth"
	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/mount"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// App assembles the whole backend. Entry points (cmd/*) build it and
// hand it to the UI shell, the Agent server or the CLI.
type App struct {
	DB       *cache.DB
	Registry *storage.Registry
	Queue    *transfer.Queue
	Worker   *transfer.Worker
	Agent    *agentapi.Client // nil when no Agent is configured

	mounts map[string]MountHandle
	// mountsMu guards mounts (and mountsLoading): since v0.18.0
	// the startup load runs in the background while the mygo
	// thread serves ListMounts concurrently, so every map access
	// — read or write — goes through it. Mount structs themselves
	// are copy-on-write on the rebuild paths (TestMount,
	// UpdateMountRoot, ...): a rebuild mutates a copy and swaps
	// the handle under the lock, never the shared pointer in
	// place. The two exceptions are UpdateMountGroup / MoveMount /
	// RemoveMount's renumbering, which mutate SortOrder in place
	// while holding the write lock (ListMounts takes the read lock,
	// so no reader can observe a half-renumbered group).
	mountsMu sync.RWMutex
	// mountsLoading reports whether a background LoadMountsAsync
	// is still running (v0.18.0): the frontend polls it while the
	// sidebar fills in mount by mount. Guarded by mountsMu.
	mountsLoading bool
	agents        map[string]*domain.Agent
	// secrets encrypts mount credentials at rest (AES-256-GCM, key
	// from the config dir's master.key). Nil until SetSecrets is
	// called at startup; cloud mounts cannot be added without it.
	secrets *auth.SecretsStore
	// loginGet/loginSet are installed by cmd/desktop (mygo shell)
	// and call mygo.App.OpenAtLogin / SetOpenAtLogin. Nil anywhere
	// else (dev shell, tests): Get/SetOpenAtLogin then report
	// honestly that the setting is unavailable in this run mode.
	loginGet func() (bool, error)
	loginSet func(bool) error
	// pickFiles is installed by cmd/desktop (mygo shell) and calls
	// mygo.Dialog.Open. Nil anywhere else (dev shell, tests):
	// PickFiles then reports honestly that the picker is
	// unavailable in this run mode.
	pickFiles func(ctx context.Context) ([]string, error)
	// quitHook is installed by cmd/desktop (mygo shell) and quits
	// the app (mygo.App.Quit). Nil anywhere else (dev shell,
	// tests, Agent): ConfirmExit(true) then only sets the flag
	// below and does nothing, instead of pretending to quit.
	quitHook func()
	// exitConfirmed is set by ConfirmExit(true) before the quit
	// hook runs, so the second Quit does not trip OnBeforeQuit
	// again (cmd/desktop reads it via ExitConfirmed). Guarded by
	// exitMu; never reset — one confirmed exit per process run.
	exitMu        sync.Mutex
	exitConfirmed bool
	// localProv is the on-demand local-filesystem provider for the
	// "local" pseudo-mount, rooted at "/" so absolute local:// paths
	// resolve. Built lazily; local jobs never leave this machine.
	localProv storage.Provider
	events    *EventBus
	// pproxy serves preview token URLs (v0.9.1): the loopback
	// preview proxy for drivers without a direct URL. Created
	// lazily by previewProxy(); never nil after first use.
	pproxy *previewProxy
	// pcache manages the preview cache (v0.9.1): downloaded copies
	// for "open with system app". Created lazily by
	// previewCacheManager(); never nil after first use.
	pcache *previewCache
	// mountSvc is the FUSE volume service (v0.19.0): the
	// ~/ZyDrive volume, per-mount switches, write-back cache
	// and upload worker. Built lazily by mountService() so
	// tests and the Agent (which never mounts) pay nothing.
	mountSvc   *mount.Service
	mountSvcMu sync.Mutex
	// transferLimiter is the single shared rate limiter for the
	// whole app (Settings speed limit is a global cap, not
	// per-transfer: 4 transfers at 1 MB/s each would otherwise
	// total 4 MB/s). Created once by ApplyTransferSettings and
	// re-rated live on every settings save.
	transferLimiter *transfer.Limiter
	// testedOK remembers configs TestMountConfig probed
	// successfully (in-component test, wizard step 2): a save
	// within 5 minutes for the same config is stored Verified
	// without probing again — the wizard must not test twice.
	// Only SHA-256 hashes of the config are kept, in memory;
	// no secret is stored or logged here.
	testedMu sync.Mutex
	testedOK map[string]time.Time
}

// Events returns the app event bus.
func (a *App) Events() *EventBus {
	if a.events == nil {
		a.events = NewEventBus()
	}
	return a.events
}

// MountHandle binds a Mount to its live provider instance.
type MountHandle struct {
	Mount    *domain.Mount
	Provider storage.Provider
}

// New returns an App. The transfer worker is started by the caller.
func New(db *cache.DB, registry *storage.Registry, queue *transfer.Queue) *App {
	return &App{
		DB:       db,
		Registry: registry,
		Queue:    queue,
		mounts:   map[string]MountHandle{},
		agents:   map[string]*domain.Agent{},
	}
}

// Mount returns the live handle for a mount ID.
func (a *App) Mount(id string) (MountHandle, bool) {
	a.mountsMu.RLock()
	defer a.mountsMu.RUnlock()
	h, ok := a.mounts[id]
	return h, ok
}

// setHandle swaps one live handle in. Callers do the slow work
// (build, probe) before this call and never hold the lock across
// it: the swap itself is the only critical section.
func (a *App) setHandle(id string, h MountHandle) {
	a.mountsMu.Lock()
	defer a.mountsMu.Unlock()
	a.mounts[id] = h
}

// listMountsLocked returns the mounts in creation order. The caller
// must hold mountsMu (read or write): ListMounts takes the read
// lock, MoveMount holds the write lock while it renumbers.
func (a *App) listMountsLocked() []*domain.Mount {
	out := make([]*domain.Mount, 0, len(a.mounts))
	for _, h := range a.mounts {
		out = append(out, h.Mount)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}
