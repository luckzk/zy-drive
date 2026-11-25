// Command mcm-desktop is entry one: the mygo desktop app.
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

	"github.com/luckzk/zy-drive/internal/app"
	"github.com/luckzk/zy-drive/internal/auth"
	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/providers/drive"
	"github.com/luckzk/zy-drive/internal/storage/providers/drime"
	"github.com/luckzk/zy-drive/internal/storage/providers/dropbox"
	"github.com/luckzk/zy-drive/internal/storage/providers/local"
	"github.com/luckzk/zy-drive/internal/storage/providers/onedrive"
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
	registry.Register(domain.ProviderLocal, local.New)

	queue := transfer.NewQueue(db)
	application := app.New(db, registry, queue)
	application.SetSecrets(secrets)

	// Restore persisted mounts (and their groups) from SQLite;
	// mounts whose credentials cannot be decrypted/rebuilt stay
	// listed with status "error" instead of disappearing.
	if err := application.LoadMounts(); err != nil {
		log.Printf("desktop: restore mounts: %v", err)
	}

	// Transfer worker: executes queued moves (cross-cloud copies and
	// drag-drop uploads) via app.ExecuteTransfer — streaming, never
	// staged on local disk.
	worker := transfer.NewWorker(queue, application.ExecuteTransfer, 2, nil)
	application.Worker = worker

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
}
