// Command mcm-desktop: mygo window shell.
//
// Wiring (verified against mygo v0.10.0 docs, docs/bindings.md,
// docs/frontend.md, docs/configuration.md and `go doc`):
//   - mygo.Bind(service) before App.Run; the service is
//     app.DesktopService (internal/app/bind.go), a dedicated type so
//     Bind never sees *App's non-JSON methods (Events, Mount...).
//   - mygo.App.WhenReady opens the window with
//     NewWindow(WindowOptions{URL: "/"}) — URLs without a scheme load
//     the frontend set by mygo.SetFrontend.
//   - SetFrontend(os.DirFS(frontendDir)) serves the built frontend
//     (frontend/dist) at mygo://localhost/; no mygo CLI build step is
//     required to run from the repo.
//   - Menu via mygo.NewMenu + App.SetMenu; tray via mygo.NewTray
//     (after ready). Both are best-effort: a tray failure only logs.
//   - App.Run must be called from main(); it blocks until quit.
//
// Fallback: when there is no system webview (mygo.WebViewAvailable
// is false — e.g. a headless Linux VM without WebKitGTK), when the
// frontend is not built, or when App.Run fails, the desktop falls
// back to the dev shell: plain HTTP on 127.0.0.1 serving the
// frontend directory plus /api/health. It never crashes the process
// for a missing webview.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"

	"github.com/luckzk/zy-drive/internal/app"
	"github.com/luckzk/zy-drive/internal/version"
)

// runDesktop starts the mygo window, or the dev shell when mygo
// cannot run here. It blocks until the app quits (mygo) or the
// context is cancelled (dev shell).
func runDesktop(application *app.App, addr string, devShell bool, frontendDir string, ctx context.Context) {
	// `mygo generate` runs main with MYGO_GENERATE set: App.Run then
	// writes the TypeScript client and returns without opening a
	// window — even with no webview. Never take the dev-shell
	// fallback in that mode, or generation hangs forever.
	if os.Getenv("MYGO_GENERATE") != "" {
		if err := runMygo(application, frontendDir, true); err != nil {
			log.Fatalf("desktop: mygo generate: %v", err)
		}
		return
	}
	if !devShell && mygo.WebViewAvailable() {
		if err := runMygo(application, frontendDir, false); err != nil {
			log.Printf("desktop: mygo failed (%v); falling back to dev shell on http://%s", err, addr)
		} else {
			return // App.Run returned cleanly after Quit.
		}
	} else if !devShell {
		log.Printf("desktop: no system webview available; falling back to dev shell on http://%s", addr)
	}
	runDevShell(addr, frontendDir, ctx)
}

func runMygo(application *app.App, frontendDir string, generate bool) error {
	if !generate {
		if _, err := os.Stat(filepath.Join(frontendDir, "index.html")); err != nil {
			return fmt.Errorf("mygo: frontend not built at %s (run: npm run build in frontend/)", frontendDir)
		}
	}
	mygo.SetFrontend(os.DirFS(frontendDir))
	mygo.App.SetName("ZyDrive")
	mygo.App.SetVersion(version.AppVersion)
	mygo.Bind(application.BindService())

	var mainWin *mygo.Window
	mygo.App.WhenReady(func() {
		mygo.App.SetMenu(buildMenu())
		if _, err := mygo.NewTray(mygo.TrayOptions{
			ID:      "zy-drive",
			Title:   "ZyDrive",
			ToolTip: "ZyDrive",
			Menu: mygo.NewMenu([]*mygo.MenuItem{
				{Label: "显示主窗口", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Show() }},
				mygo.Separator(),
				{Label: "退出", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
			}),
		}); err != nil {
			log.Printf("desktop: tray unavailable: %v", err)
		}
		mainWin = mygo.NewWindow(mygo.WindowOptions{
			Title: "ZyDrive", URL: "/",
			Width: 1280, Height: 800, MinWidth: 960, MinHeight: 600,
			StateKey: "main",
		})
		_ = mainWin
	})
	mygo.App.OnWindowAllClosed(func() { mygo.App.Quit() })
	return mygo.App.Run()
}

func buildMenu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Label: "文件", Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleReload},
			mygo.Separator(),
			{Role: mygo.RoleClose},
		}},
		{Role: mygo.RoleEditMenu},
		{Label: "视图", Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleReload},
			{Role: mygo.RoleToggleDevTools},
			mygo.Separator(),
			{Role: mygo.RoleZoomIn},
			{Role: mygo.RoleZoomOut},
			{Role: mygo.RoleResetZoom},
			mygo.Separator(),
			{Role: mygo.RoleToggleFullScreen},
		}},
		{Role: mygo.RoleWindow},
	})
}

// runDevShell serves the frontend over plain HTTP (the pre-mygo
// behaviour). Used with --dev-shell, or as the mygo fallback.
func runDevShell(addr, frontendDir string, ctx context.Context) {
	if _, err := os.Stat(frontendDir); os.IsNotExist(err) {
		frontendDir = "frontend"
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(frontendDir)))
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"version":%q}`, version.AppVersion)
	})

	log.Printf("zy-drive desktop %s — dev shell on http://%s", version.AppVersion, addr)
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("desktop: http: %v", err)
		}
	}()
	<-ctx.Done()
	_ = srv.Shutdown(context.Background())
	log.Println("desktop: stopped")
}
