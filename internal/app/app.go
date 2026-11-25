// SPDX-License-Identifier: MPL-2.0
// Package app is the application layer: the only business-orchestration
// entry point. The UI (frontend via mygo.Bind), the CLI and the Agent
// all call into App. App only talks to the storage.Provider interface,
// the sync/transfer engines and the cache — it never imports a concrete
// driver package and never contains a vendor name.
package app

import (
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/agentapi"
	"github.com/luckzk/zy-drive/internal/auth"
	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
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
	agents map[string]*domain.Agent
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
	// localProv is the on-demand local-filesystem provider for the
	// "local" pseudo-mount, rooted at "/" so absolute local:// paths
	// resolve. Built lazily; local jobs never leave this machine.
	localProv storage.Provider
	events    *EventBus
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
	h, ok := a.mounts[id]
	return h, ok
}
