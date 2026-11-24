// Command mcm-agent is entry two: the headless Agent.
// It runs the Agent API (five core endpoints + SSE) and the in-process
// scheduler. It has no UI. It must sit behind HTTPS (reverse proxy
// terminates TLS) with an API token — the port is never exposed bare.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/luckzk/zy-drive/internal/agentapi"
	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/platform/scheduler"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/providers/drive"
	"github.com/luckzk/zy-drive/internal/storage/providers/dropbox"
	"github.com/luckzk/zy-drive/internal/storage/providers/local"
	"github.com/luckzk/zy-drive/internal/storage/providers/s3"
	"github.com/luckzk/zy-drive/internal/version"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	token := flag.String("token", "", "API token (required in production; empty only for local dev)")
	configDir := flag.String("config-dir", "", "configuration directory (default ~/.config/zy-drive-agent)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("zy-drive agent %s (protocol %s)\n", version.AppVersion, version.ProtocolVersion)
		return
	}
	if *token == "" {
		log.Println("agent: WARNING: no --token given; API auth is disabled (local dev only)")
	}

	dir := *configDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("agent: home dir: %v", err)
		}
		dir = filepath.Join(home, ".config", "zy-drive-agent")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatalf("agent: mkdir %s: %v", dir, err)
	}

	db, err := cache.Open(filepath.Join(dir, "agent.db"))
	if err != nil {
		log.Fatalf("agent: open db: %v", err)
	}
	defer db.Close()

	// Same driver registry as the desktop: one codebase, two entries.
	registry := storage.Default()
	registry.Register(domain.ProviderS3, s3.New)
	registry.Register(domain.ProviderDropbox, dropbox.New)
	registry.Register(domain.ProviderDrive, drive.New)
	registry.Register(domain.ProviderLocal, local.New)

	// Scheduler: jobs are stored in the DB; the scheduler only triggers.
	// V0.1 wires the loop; job execution via the sync runner lands in V0.2.
	sched := scheduler.New()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go sched.Start(ctx)

	srv := agentapi.NewServer(*token)
	httpSrv := &http.Server{
		Addr:         *addr,
		Handler:      srv.Handler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE streams stay open
	}
	log.Printf("zy-drive agent %s (protocol %s) on http://%s", version.AppVersion, version.ProtocolVersion, *addr)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("agent: http: %v", err)
		}
	}()
	<-ctx.Done()
	_ = httpSrv.Shutdown(context.Background())
	log.Println("agent: stopped")
}
