// SPDX-License-Identifier: MPL-2.0
// Command zy-drive-desktop is entry one: the mygo desktop app.
// It only assembles dependencies and starts: configuration loading,
// provider registration, app construction, worker start, then the
// mygo window (cmd/desktop/mygo.go) with a dev-shell fallback when
// no system webview is available.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/luckzk/zy-drive/internal/app"
	"github.com/luckzk/zy-drive/internal/auth"
	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/providers/box"
	"github.com/luckzk/zy-drive/internal/storage/providers/drime"
	"github.com/luckzk/zy-drive/internal/storage/providers/drive"
	"github.com/luckzk/zy-drive/internal/storage/providers/dropbox"
	"github.com/luckzk/zy-drive/internal/storage/providers/local"
	"github.com/luckzk/zy-drive/internal/storage/providers/mega"
	"github.com/luckzk/zy-drive/internal/storage/providers/onedrive"
	"github.com/luckzk/zy-drive/internal/storage/providers/pcloud"
	"github.com/luckzk/zy-drive/internal/storage/providers/s3"
	"github.com/luckzk/zy-drive/internal/transfer"
	"github.com/luckzk/zy-drive/internal/version"
)

func main() {
	configDir := flag.String("config-dir", "", "configuration directory (default ~/.config/zy-drive)")
	addr := flag.String("addr", "127.0.0.1:18080", "dev-shell listen address")
	frontendDir := flag.String("frontend-dir", "frontend/dist", "built frontend directory served by mygo/dev shell")
	devShell := flag.Bool("dev-shell", false, "skip the mygo window; serve the frontend over plain HTTP")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("zy-drive desktop %s (protocol %s)\n", version.AppVersion, version.ProtocolVersion)
		return
	}

	dir := *configDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("desktop: home dir: %v", err)
		}
		dir = filepath.Join(home, ".config", "zy-drive")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatalf("desktop: mkdir %s: %v", dir, err)
	}

	db, err := cache.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		log.Fatalf("desktop: open db: %v", err)
	}
	defer db.Close()

	// Master key for encrypted credential storage: generated once
	// into <configDir>/master.key (0600) on first run, never
	// hard-coded, never committed.
	secrets, err := auth.LoadOrCreateMasterKey(dir)
	if err != nil {
		log.Fatalf("desktop: master key: %v", err)
	}

	// Register drivers: the single assembly point (storage.Registry).
	// One factory per kind; instances are built per Mount in
	// app.AddMount, so two accounts of the same kind never share a
	// token, cache or cursor.
	registry := storage.Default()
	registry.Register(domain.ProviderS3, s3.New)
	registry.Register(domain.ProviderDropbox, dropbox.New)
	registry.Register(domain.ProviderDrive, drive.New)
	registry.Register(domain.ProviderDrime, drime.New)
	registry.Register(domain.ProviderOneDrive, onedrive.New)
	registry.Register(domain.ProviderBox, box.New)
	registry.Register(domain.ProviderPCloud, pcloud.New)
	registry.Register(domain.ProviderMega, mega.New)
	registry.Register(domain.ProviderLocal, local.New)

	queue := transfer.NewQueue(db)
	application := app.New(db, registry, queue)
	application.SetSecrets(secrets)

	// Builtin shared OAuth apps (B, one-click authorization): the
	// IDs live in internal/app/builtin-oauth-clients.json, embedded
	// into the binary (see internal/app/builtin_oauth.go). Nothing
	// to load from disk here — a missing or malformed embedded file
	// falls back to the code constants, never a fatal error.
	//
	// The user's OWN OAuth apps live separately in
	// <configDir>/custom-oauth-clients.json (see internal/app/
	// custom_oauth.go). The old oauth-clients.json override file —
	// which used to overwrite the builtin per provider — is
	// migrated into the custom store on startup and then retired
	// (renamed to .migrated), so its values are never lost and
	// never again applied as a builtin override.
	if n, err := application.MigrateLegacyOAuthClients(filepath.Join(dir, "oauth-clients.json"), filepath.Join(dir, "custom-oauth-clients.json")); err != nil {
		log.Printf("desktop: oauth-clients.json migration: %v", err)
	} else if n > 0 {
		log.Printf("desktop: migrated %d provider(s) from oauth-clients.json into custom-oauth-clients.json (builtin untouched)", n)
	}
	if err := application.LoadCustomOAuthClients(filepath.Join(dir, "custom-oauth-clients.json")); err != nil {
		log.Printf("desktop: custom-oauth-clients.json: %v (own apps unavailable this run)", err)
	}

	// Apply the saved proxy settings before any outbound request:
	// a manual proxy stored in Settings must be live from the
	// first List/Quota call, not only after the user re-saves.
	if err := application.ApplyProxySettings(); err != nil {
		log.Printf("desktop: proxy settings: %v (falling back to system proxy)", err)
	}

	// Restore persisted mounts (and their groups) from SQLite;
	// mounts whose credentials cannot be decrypted/rebuilt stay
	// listed with status "error" instead of disappearing.
	//
	// v0.18.0: this runs in the BACKGROUND, after the window is
	// up — LoadMountsAsync returns immediately and the mounts
	// appear as they finish (frontend polls MountsLoading +
	// ListMounts). The old synchronous call froze startup
	// completely on 2026-11-27: one MEGA mount's LoginWithKeys
	// dialled direct and hung before any window existed, with no
	// way into Settings to fix the proxy. Each mount gets 30s
	// and the whole load 120s; a mount that times out is listed
	// failed with its reason, never dropped.
	go application.LoadMountsAsync()

	// FUSE mount volume ~/ZyDrive (v0.19.0): reconcile the
	// write-back cache with disk and start its upload worker.
	// A failure is only logged — a stale cache must never block
	// startup, and the volume itself only mounts on demand
	// (at least one switch on).
	if err := application.InitMountService(); err != nil {
		log.Printf("desktop: mount service: %v", err)
	}

	// Restore the persisted mount switches once the background
	// load finishes: startup order is window → mounts loaded →
	// switches restored → volume mounted. A switch whose mount
	// failed to load stays on (card red, human reason) — the
	// restore never silently switches a mount off.
	go func() {
		deadline := time.Now().Add(150 * time.Second)
		for application.MountsLoading() && time.Now().Before(deadline) {
			time.Sleep(500 * time.Millisecond)
		}
		application.RestoreMounts()
	}()

	// Preview cache (v0.9.1): reconcile the manifest with disk
	// and trim to the configured limit. A stale cache must never
	// block startup, so a failure is only logged.
	if err := application.InitPreviewCache(); err != nil {
		log.Printf("desktop: preview cache: %v", err)
	}

	// Transfer worker: executes queued moves (cross-cloud copies and
	// drag-drop uploads) via app.ExecuteTransfer — streaming, never
	// staged on local disk. Concurrency and the speed limit come
	// from Settings (V0.9.0): ApplyTransferSettings pushes the
	// stored values in now and on every later settings save.
	worker := transfer.NewWorker(queue, application.ExecuteTransfer, 2, nil)
	application.Worker = worker
	application.ApplyTransferSettings()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go worker.Start(ctx)

	// Restore interrupted transfers from the previous run.
	if items, err := queue.RestoreInterrupted(); err != nil {
		log.Printf("desktop: restore interrupted: %v", err)
	} else if len(items) > 0 {
		log.Printf("desktop: %d interrupted transfer(s) restored", len(items))
	}

	// mygo window (menu/tray/Bind), falling back to the dev shell
	// when no system webview is available. Blocks until quit (mygo)
	// or signal (dev shell).
	runDesktop(application, *addr, *devShell, *frontendDir, ctx)

	// FUSE volume shutdown (v0.19.0): flush dirty mount-cache
	// files (30s best-effort — leftovers stay cached for next
	// start) and unmount ~/ZyDrive. When files are still open
	// the unmount is refused and the error names the count;
	// the process exit then leaves a stale volume, which the
	// next start force-unmounts before re-mounting.
	if err := application.ShutdownMounts(); err != nil {
		log.Printf("desktop: shutdown mounts: %v", err)
	}

	// "退出时清理缓存" (Settings → 缓存): mygo has no OnExit
	// hook, so the cleanup runs here, after the window loop
	// returns on quit. When the setting is off it is a no-op.
	if err := application.ClearPreviewCacheOnExit(); err != nil {
		log.Printf("desktop: clear preview cache on exit: %v", err)
	}
}
