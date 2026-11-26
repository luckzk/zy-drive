// SPDX-License-Identifier: MPL-2.0
// Command zy-drive-desktop: mygo window shell.
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
//     The tray icon and the Dock icon come from resources/icon.png
//     (loadAppIcon); a missing icon only means a title-only tray
//     item and a generic Dock icon, never a crash.
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

// loadAppIcon reads the app icon PNG (the user's green cloud,
// frontend/public/logo.svg exported to PNG) for the macOS menu-bar
// tray icon and the Dock icon. Candidates, in order:
//   - resources/icon.png next to the repo (running from source)
//   - <exe dir>/../Resources/icon.png (inside ZyDrive.app:
//     Contents/MacOS/zy-drive-desktop -> Contents/Resources/)
//   - <exe dir>/icon.png (a bare binary next to its icon)
//
// The PNG is binary and cannot be pushed to the repo — the user
// regenerates it once on their Mac with
// `node scripts/visual-check/export-logo-png.mjs`. Missing icon
// is best-effort only: the tray falls back to a title-only item
// and the Dock keeps its generic icon, never a crash.
func loadAppIcon() ([]byte, error) {
	candidates := []string{"resources/icon.png"}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "..", "Resources", "icon.png"),
			filepath.Join(dir, "icon.png"),
		)
	}
	var lastErr error
	for _, p := range candidates {
		if b, err := os.ReadFile(p); err == nil {
			return b, nil
		} else {
			lastErr = err
		}
	}
	return nil, lastErr
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
	// Open-at-login goes through mygo (login item on macOS 13+,
	// launch agent on macOS 12, XDG autostart on Linux, Run entry on
	// Windows). internal/app stays mygo-free: these hooks are the
	// only bridge, and without them the setting reports unavailable.
	application.SetLoginHooks(
		func() (bool, error) { return mygo.App.OpenAtLogin(), nil },
		func(on bool) error { return mygo.App.SetOpenAtLogin(on) },
	)
	mygo.Bind(application.BindService())

	// File preview (v0.9.1): the loopback preview proxy is served
	// as the "zydrive-preview" custom scheme, not http://127.0.0.1
	// — the frontend loads from mygo://localhost/ and a plain-http
	// subresource from it is mixed content the webview may refuse.
	// Schemes must be registered before the windows that use them
	// are created, so this sits before WhenReady. A failure here
	// only disables proxy previews (direct URLs still work).
	if err := mygo.Protocol.HandleFunc("zydrive-preview", application.PreviewHandler().ServeHTTP); err != nil {
		log.Printf("desktop: preview scheme: %v (proxy previews unavailable)", err)
	}

	var mainWin *mygo.Window
	// The menu-bar tray icon and the Dock icon come from the same
	// PNG (the user's cloud). Both are best-effort: on a machine
	// without resources/icon.png the app still runs — the tray
	// shows its title only, the Dock keeps the generic icon.
	iconPNG, iconErr := loadAppIcon()
	if iconErr != nil {
		log.Printf("desktop: no app icon found (run: node scripts/visual-check/export-logo-png.mjs): %v", iconErr)
	}
	mygo.App.WhenReady(func() {
		mygo.App.SetMenu(buildMenu())
		if iconPNG != nil {
			// Dock.SetIcon is a no-op off macOS (mygo v0.10.0).
			if err := mygo.App.Dock.SetIcon(iconPNG); err != nil {
				log.Printf("desktop: dock icon: %v", err)
			}
		}
		if _, err := mygo.NewTray(mygo.TrayOptions{
			ID:      "zy-drive",
			Icon:    iconPNG,
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
