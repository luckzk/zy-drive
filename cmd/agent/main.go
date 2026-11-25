// SPDX-License-Identifier: MPL-2.0
// Command zy-drive-agent is entry two: the headless Agent.
// It runs the Agent API (jobs, runs, SSE) and the in-process
// scheduler, and it executes the sync jobs stored in its own
// database. It has no UI. It must sit behind HTTPS (reverse proxy
// terminates TLS) with an API token — the port is never exposed bare.
//
// Self-hosted model (docs/monetization.md): the Agent runs on the
// customer's own server. Credentials reach it only one way: the
// user exports an encrypted config bundle on the desktop, copies
// the file to the server themselves, and runs
// `zy-drive-agent import --file <bundle>` here. There is no central
// server, no account system, and no credential ever travels over
// the Agent API (jobs are pushed as config only).
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
	"strings"
	"syscall"
	"time"

	"github.com/luckzk/zy-drive/internal/agentapi"
	"github.com/luckzk/zy-drive/internal/agentrun"
	"github.com/luckzk/zy-drive/internal/app"
	"github.com/luckzk/zy-drive/internal/auth"
	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/platform/scheduler"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/providers/drive"
	"github.com/luckzk/zy-drive/internal/storage/providers/drime"
	"github.com/luckzk/zy-drive/internal/storage/providers/dropbox"
	"github.com/luckzk/zy-drive/internal/storage/providers/local"
	"github.com/luckzk/zy-drive/internal/storage/providers/onedrive"
	"github.com/luckzk/zy-drive/internal/storage/providers/s3"
	"github.com/luckzk/zy-drive/internal/transfer"
	"github.com/luckzk/zy-drive/internal/version"
	"golang.org/x/term"
)

func main() {
	log.SetPrefix("zy-drive-agent: ")
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "import":
			runImport(os.Args[2:])
			return
		case "version", "--version", "-version":
			fmt.Printf("zy-drive agent %s (protocol %s)\n", version.AppVersion, version.ProtocolVersion)
			return
		case "help", "--help", "-h":
			printUsage()
			return
		}
	}
	runServer(os.Args[1:])
}

func printUsage() {
	fmt.Println("用法：")
	fmt.Println("  zy-drive-agent [--addr 127.0.0.1:8080] [--token <token>] [--config-dir <dir>]")
	fmt.Println("      启动 Agent API + 定时调度器（默认只监听本机 8080）。")
	fmt.Println("  zy-drive-agent import --file <bundle.json> [--passphrase <口令>] [--config-dir <dir>]")
	fmt.Println("      导入桌面导出的加密配置包（网盘 + 同步任务）。口令不给时交互输入（不回显），")
	fmt.Println("      也可用环境变量 ZY_DRIVE_BUNDLE_PASSPHRASE。")
	fmt.Println("  zy-drive-agent version")
	fmt.Println("环境变量：ZY_DRIVE_AGENT_TOKEN（API 令牌；旧名 MCM_AGENT_TOKEN 仍可用）。")
}

// configDir resolves --config-dir or the default.
func configDir(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("home dir: %v", err)
	}
	return filepath.Join(home, ".config", "zy-drive-agent")
}

// buildAgentApp assembles the shared App over the Agent's own
// database: master key, mounts rebuilt from SQLite, settings
// applied. The Agent is an App without a UI.
func buildAgentApp(dir string) (*app.App, *cache.DB, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	secrets, err := auth.LoadOrCreateMasterKey(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("master key: %w", err)
	}
	db, err := cache.Open(filepath.Join(dir, "agent.db"))
	if err != nil {
		return nil, nil, fmt.Errorf("open db: %w", err)
	}
	registry := storage.Default()
	registry.Register(domain.ProviderS3, s3.New)
	registry.Register(domain.ProviderDropbox, dropbox.New)
	registry.Register(domain.ProviderDrive, drive.New)
	registry.Register(domain.ProviderDrime, drime.New)
	registry.Register(domain.ProviderOneDrive, onedrive.New)
	registry.Register(domain.ProviderLocal, local.New)
	a := app.New(db, registry, transfer.NewQueue(db))
	a.SetSecrets(secrets)
	if err := a.LoadMounts(); err != nil {
		log.Printf("restore mounts: %v", err)
	}
	return a, db, nil
}

// runImport implements `zy-drive-agent import`.
func runImport(args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	file := fs.String("file", "", "加密配置包文件（桌面端导出）")
	passphrase := fs.String("passphrase", "", "配置包口令（不给则交互输入；也可用 ZY_DRIVE_BUNDLE_PASSPHRASE）")
	dirFlag := fs.String("config-dir", "", "configuration directory (default ~/.config/zy-drive-agent)")
	_ = fs.Parse(args)
	if strings.TrimSpace(*file) == "" {
		log.Fatal("import: 需要 --file <bundle.json>")
	}
	pass := strings.TrimSpace(*passphrase)
	if pass == "" {
		pass = strings.TrimSpace(os.Getenv("ZY_DRIVE_BUNDLE_PASSPHRASE"))
	}
	if pass == "" {
		fmt.Print("配置包口令：")
		b, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println()
		if err != nil {
			log.Fatalf("import: 读口令失败：%v", err)
		}
		pass = strings.TrimSpace(string(b))
	}
	if pass == "" {
		log.Fatal("import: 口令不能为空")
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		log.Fatalf("import: 读 %s 失败：%v", *file, err)
	}
	dir := configDir(*dirFlag)
	application, db, err := buildAgentApp(dir)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	runner := agentrun.New(application, db, scheduler.New())
	bundle, err := runner.ImportBundle(data, pass)
	if err != nil {
		log.Fatalf("import: %v", err)
	}
	fmt.Printf("已导入配置包（导出于 %s）：\n", bundle.ExportedAt.Format("2006-01-02 15:04"))
	for _, bm := range bundle.Mounts {
		fmt.Printf("  网盘：%s（%s）\n", bm.Mount.DisplayName, bm.Mount.Provider)
	}
	for _, j := range bundle.Jobs {
		sched := j.Schedule
		if sched == "" {
			sched = "手动（不定时）"
		}
		fmt.Printf("  任务：%s（%s，%s）\n", j.Name, j.Direction, sched)
	}
	fmt.Printf("共 %d 个网盘、%d 个任务。定时任务已接上调度器，Agent 启动后按时跑。\n", len(bundle.Mounts), len(bundle.Jobs))
}

// runServer implements the default mode: API + scheduler.
func runServer(args []string) {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "listen address")
	token := fs.String("token", "", "API token (required in production; empty only for local dev)")
	dirFlag := fs.String("config-dir", "", "configuration directory (default ~/.config/zy-drive-agent)")
	showVersion := fs.Bool("version", false, "print version and exit")
	_ = fs.Parse(args)
	if *showVersion {
		fmt.Printf("zy-drive agent %s (protocol %s)\n", version.AppVersion, version.ProtocolVersion)
		return
	}
	apiToken := strings.TrimSpace(*token)
	if apiToken == "" {
		apiToken = strings.TrimSpace(os.Getenv("ZY_DRIVE_AGENT_TOKEN"))
	}
	if apiToken == "" {
		if legacy := strings.TrimSpace(os.Getenv("MCM_AGENT_TOKEN")); legacy != "" {
			log.Println("WARNING: MCM_AGENT_TOKEN is deprecated, use ZY_DRIVE_AGENT_TOKEN")
			apiToken = legacy
		}
	}
	if apiToken == "" {
		log.Println("WARNING: no --token given and ZY_DRIVE_AGENT_TOKEN is unset; API auth is disabled (local dev only)")
	}

	dir := configDir(*dirFlag)
	application, db, err := buildAgentApp(dir)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Transfer worker: queued moves (batch copies the Agent itself
	// enqueues, e.g. sync plan items go through the sync runner,
	// not this queue — the worker is for ad-hoc transfers) run with
	// the stored concurrency/limit (defaults 4 / unlimited).
	worker := transfer.NewWorker(application.Queue, application.ExecuteTransfer, 2, nil)
	application.Worker = worker
	application.ApplyTransferSettings()
	application.ApplyProxySettings()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go worker.Start(ctx)
	if items, err := application.Queue.RestoreInterrupted(); err != nil {
		log.Printf("restore interrupted: %v", err)
	} else if len(items) > 0 {
		log.Printf("%d interrupted transfer(s) restored", len(items))
	}

	// Scheduler + runner: stored jobs fire on their own clock;
	// every run is recorded in agent.db and visible to the desktop.
	sched := scheduler.New()
	runner := agentrun.New(application, db, sched)
	srv := agentapi.NewServer(apiToken)
	if err := srv.SetStore(db); err != nil {
		log.Fatalf("load jobs: %v", err)
	}
	srv.SetExecutor(runner)
	runner.SetPublish(srv.Publish)
	if err := runner.RegisterJobs(); err != nil {
		log.Fatalf("register jobs: %v", err)
	}
	go sched.Start(ctx)

	httpSrv := &http.Server{
		Addr:         *addr,
		Handler:      srv.Handler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE streams stay open
	}
	log.Printf("zy-drive agent %s (protocol %s) on http://%s", version.AppVersion, version.ProtocolVersion, *addr)
	log.Printf("config dir: %s (import a bundle with: zy-drive-agent import --file <bundle.json>)", dir)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http: %v", err)
		}
	}()
	<-ctx.Done()
	_ = httpSrv.Shutdown(context.Background())
	log.Println("stopped")
}
