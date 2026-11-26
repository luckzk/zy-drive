// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// preview_open.go — the "open with system app" tier of file
// preview (v0.9.1). The file is first downloaded to the preview
// cache (preview_cache.go — the cloud original is never touched,
// the copy is read-only), then opened with the OS default app:
//   - macOS: Quick Look first (`qlmanage -p`), because it previews
//     without starting the full app; when Quick Look cannot open
//     the format, fall back to `open` (the default app). Whether
//     Quick Look can open it is probed with `qlmanage -t`
//     (thumbnail generation, exit 0 = supported).
//   - Windows: ShellExecute via `cmd /c start`.
//   - Linux: `xdg-open`.
// "在 Finder/资源管理器中显示" reveals the cached copy: macOS
// `open -R` (selects the file), Windows `explorer /select,`
// (selects it), Linux `xdg-open` on the parent folder (no
// select support there — it opens the folder).

// OpenWithSystemApp downloads a file to the preview cache (or
// reuses the cached copy) and opens it with the system default
// app. It returns the local cached path, so the caller can say
// where the copy lives.
func (a *App) OpenWithSystemApp(ctx context.Context, mountID, ref string) (string, error) {
	path, err := a.previewCacheEnsure(ctx, mountID, ref)
	if err != nil {
		return "", err
	}
	if err := openWithSystemApp(path); err != nil {
		return "", fmt.Errorf("app: 打不开这个文件：%w", err)
	}
	return path, nil
}

// RevealInFinder downloads a file to the preview cache (or reuses
// the cached copy) and shows it in the system file manager. It
// returns the local cached path.
func (a *App) RevealInFinder(ctx context.Context, mountID, ref string) (string, error) {
	path, err := a.previewCacheEnsure(ctx, mountID, ref)
	if err != nil {
		return "", err
	}
	if err := revealInFinder(path); err != nil {
		return "", fmt.Errorf("app: 打不开所在文件夹：%w", err)
	}
	return path, nil
}

func openWithSystemApp(path string) error {
	switch runtime.GOOS {
	case "darwin":
		// Quick Look first: a fast preview without starting the
		// full app. qlmanage -p blocks while its window is open,
		// so it is Started, never Waited on.
		if quickLookCanOpen(path) {
			cmd := exec.Command("qlmanage", "-p", path)
			if err := cmd.Start(); err == nil {
				return nil
			}
			// Start failed (no qlmanage?): fall through to open.
		}
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", "", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

// quickLookCanOpen probes whether Quick Look can preview a file:
// `qlmanage -t` generates a thumbnail and exits 0 when the format
// is supported. 15 seconds is generous; a slow first thumbnail
// still beats opening the wrong app.
func quickLookCanOpen(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "qlmanage", "-t", path).Run() == nil
}

func revealInFinder(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", "-R", path).Start()
	case "windows":
		return exec.Command("explorer", "/select,"+path).Start()
	default:
		return exec.Command("xdg-open", filepath.Dir(path)).Start()
	}
}
