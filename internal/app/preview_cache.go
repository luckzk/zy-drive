// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// preview_cache.go — the preview cache manager (v0.9.1).
// Only the "open with system app" tier downloads copies here;
// in-app preview never touches disk (see preview.go).
//
// Rules (docs/roadmap-next.md, "预览缓存管理规则", user-confirmed
// 2026-11-25):
//   - Location: the platform app cache dir (macOS
//     ~/Library/Caches/com.luckzk.zy-drive/preview, Windows
//     %LOCALAPPDATA%/zy-drive/preview, Linux ~/.cache/zy-drive/
//     preview). Key: sha256(mount_id + "\x00" + ref), so two
//     mounts with the same file name never collide. The file keeps
//     its original name (sanitized) after the key, so the OS picks
//     the right app by extension.
//   - Manifest: the preview_cache table in app.db (size, remote
//     mtime, last access). Reconciled with disk at startup: a row
//     without a file is dropped, a file without a row (including
//     orphan .part temp files) is deleted.
//   - Atomic writes: temp file + rename, so a half download is
//     never mistaken for a complete cache copy. Cached files are
//     set read-only (0444) after writing — the user must not edit
//     a preview copy thinking it syncs back to the cloud.
//   - Reuse: same mount+path with matching size AND remote mtime
//     is reused without re-downloading; a changed remote file is
//     re-downloaded over it.
//   - Eviction: over the limit (default 2 GB, Settings 500 MB /
//     1 GB / 2 GB / 5 GB), evict by last access (LRU); files
//     accessed within the last 5 minutes are never evicted (a
//     file open in the system app right now cannot be deleted
//     under it). Shrinking the limit and startup both trim.
//   - "清理缓存" clears ONLY this cache — never mount metadata,
//     sync jobs, or the local decrypted index.

// previewCacheLimitDefaultMB is the default cache cap (2 GB).
const previewCacheLimitDefaultMB = 2048

// previewCacheRecentGuard is the LRU protection window: files
// accessed within this long are never evicted.
const previewCacheRecentGuard = 5 * time.Minute

// previewCacheDir returns the preview cache directory for this
// platform. UserCacheDir already points at the platform cache
// root; only the leaf differs (Apple convention vs the plain
// zy-drive name elsewhere).
func previewCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", fmt.Errorf("app: 找不到缓存目录：%w", err)
		}
		base = filepath.Join(home, ".cache")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(base, "com.luckzk.zy-drive", "preview"), nil
	}
	return filepath.Join(base, "zy-drive", "preview"), nil
}

// previewCacheKey is sha256(mountID + "\x00" + ref), hex.
func previewCacheKey(mountID, ref string) string {
	sum := sha256.Sum256([]byte(mountID + "\x00" + ref))
	return hex.EncodeToString(sum[:])
}

// previewCacheFilename keeps the original name (so the OS picks
// the right app) after the key. Unsafe characters become '_';
// the whole name is capped so key + name never exceeds the
// filesystem's filename limit.
func previewCacheFilename(key, name string) string {
	base := filepath.Base(name)
	var b strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	safe := b.String()
	if safe == "" || safe == "." || safe == ".." {
		safe = "file"
	}
	fn := key + "_" + safe
	if len(fn) > 200 {
		fn = fn[:200]
	}
	return fn
}

// previewCache is one App's cache manager. All methods are safe
// for concurrent use (one mutex serializes downloads; the
// manifest itself is SQLite).
type previewCache struct {
	mu  sync.Mutex
	dir string
	db  *cache.DB
}

// previewCacheManager returns the App's cache manager, creating
// the directory on first use. It does NOT reconcile or trim —
// callers that want startup discipline use InitPreviewCache.
func (a *App) previewCacheManager() (*previewCache, error) {
	if a.DB == nil {
		return nil, fmt.Errorf("app: 本地数据库还没初始化，预览缓存不可用")
	}
	if a.pcache != nil {
		return a.pcache, nil
	}
	dir, err := previewCacheDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("app: 建预览缓存目录失败：%w", err)
	}
	a.pcache = &previewCache{dir: dir, db: a.DB}
	return a.pcache, nil
}

// InitPreviewCache reconciles the manifest with disk and trims to
// the current limit. Called once at desktop startup (a failure is
// logged, never fatal — a stale cache must not block the app) and
// lazily by the first cache use.
func (a *App) InitPreviewCache() error {
	pc, err := a.previewCacheManager()
	if err != nil {
		return err
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if err := pc.reconcileLocked(); err != nil {
		return err
	}
	return pc.trimLocked(a.previewCacheLimitMB())
}

// previewCacheLimitMB reads the current cap from Settings
// (default 2 GB when unset or unreadable).
func (a *App) previewCacheLimitMB() int {
	s, err := a.GetSettings()
	if err != nil {
		return previewCacheLimitDefaultMB
	}
	if s.PreviewCacheLimitMB <= 0 {
		return previewCacheLimitDefaultMB
	}
	return s.PreviewCacheLimitMB
}

// reconcileLocked drops manifest rows whose file is gone and
// deletes files (including orphan .part temps) with no row. The
// caller holds pc.mu.
func (pc *previewCache) reconcileLocked() error {
	entries, err := pc.db.ListPreviewEntries()
	if err != nil {
		return err
	}
	known := map[string]bool{}
	var firstErr error
	for _, e := range entries {
		known[e.Filename] = true
		if _, err := os.Stat(filepath.Join(pc.dir, e.Filename)); err != nil {
			if err := pc.db.DeletePreviewEntry(e.Key); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	files, err := os.ReadDir(pc.dir)
	if err != nil {
		if firstErr == nil {
			firstErr = err
		}
		return firstErr
	}
	for _, f := range files {
		if f.IsDir() || known[f.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(pc.dir, f.Name())); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// trimLocked evicts least-recently-used entries until the total is
// under limitMB, never evicting files accessed within the recent
// guard. The caller holds pc.mu.
func (pc *previewCache) trimLocked(limitMB int) error {
	if limitMB <= 0 {
		return nil
	}
	limit := int64(limitMB) * 1024 * 1024
	entries, err := pc.db.ListPreviewEntries()
	if err != nil {
		return err
	}
	var total int64
	for _, e := range entries {
		total += e.Size
	}
	if total <= limit {
		return nil
	}
	cutoff := time.Now().Add(-previewCacheRecentGuard)
	for _, e := range entries {
		if total <= limit {
			break
		}
		if e.LastAccess.After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(pc.dir, e.Filename))
		if err := pc.db.DeletePreviewEntry(e.Key); err != nil {
			return err
		}
		total -= e.Size
	}
	return nil
}

// ensure returns the cached local path for one remote file,
// downloading it (through the shared limiter, atomically) when
// the cache has no fresh copy. reused reports a cache hit.
func (pc *previewCache) ensure(ctx context.Context, provider storage.Provider, limiter *transfer.Limiter, mountID, ref string, size int64, mtime time.Time) (path string, reused bool, err error) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	key := previewCacheKey(mountID, ref)
	name := filepath.Base(ref)
	filename := previewCacheFilename(key, name)
	full := filepath.Join(pc.dir, filename)

	if e, err := pc.db.LoadPreviewEntry(key); err == nil && e != nil {
		if e.Size == size && e.RemoteMtime.Equal(mtime) {
			if fi, serr := os.Stat(full); serr == nil && fi.Size() == size {
				now := time.Now().UTC()
				e.LastAccess = now
				_ = pc.db.SavePreviewEntry(e)
				return full, true, nil
			}
		}
	}

	rc, err := provider.OpenRead(ctx, ref)
	if err != nil {
		return "", false, fmt.Errorf("app: 下载预览副本失败：%w", err)
	}
	defer rc.Close()
	body := io.Reader(rc)
	if limiter != nil {
		body = transfer.NewLimitedReader(rc, limiter)
	}
	var randBytes [3]byte
	_, _ = rand.Read(randBytes[:])
	tmp := full + ".part-" + hex.EncodeToString(randBytes[:])
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "", false, fmt.Errorf("app: 写预览缓存失败：%w", err)
	}
	_, copyErr := io.Copy(f, body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return "", false, fmt.Errorf("app: 下载预览副本失败：%w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return "", false, fmt.Errorf("app: 写预览缓存失败：%w", closeErr)
	}
	// Read-only after writing: a preview copy must never be
	// edited in place under the impression it syncs back.
	if err := os.Chmod(tmp, 0o444); err != nil {
		_ = os.Remove(tmp)
		return "", false, fmt.Errorf("app: 写预览缓存失败：%w", err)
	}
	if err := os.Rename(tmp, full); err != nil {
		_ = os.Remove(tmp)
		return "", false, fmt.Errorf("app: 写预览缓存失败：%w", err)
	}
	now := time.Now().UTC()
	if err := pc.db.SavePreviewEntry(&cache.PreviewEntry{
		Key: key, MountID: mountID, Ref: ref, Filename: filename,
		Size: size, RemoteMtime: mtime, LastAccess: now,
	}); err != nil {
		_ = os.Remove(full)
		return "", false, fmt.Errorf("app: 写预览缓存失败：%w", err)
	}
	return full, false, nil
}

// infoLocked sums the cache as it stands on disk (manifest rows
// whose file exists). The caller holds pc.mu.
func (pc *previewCache) infoLocked() (sizeBytes int64, count int, err error) {
	entries, err := pc.db.ListPreviewEntries()
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		fi, serr := os.Stat(filepath.Join(pc.dir, e.Filename))
		if serr != nil {
			continue
		}
		sizeBytes += fi.Size()
		count++
	}
	return sizeBytes, count, nil
}

// clearLocked deletes every cached file and the whole manifest.
// The caller holds pc.mu.
func (pc *previewCache) clearLocked() (freedBytes int64, count int, err error) {
	entries, err := pc.db.ListPreviewEntries()
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		full := filepath.Join(pc.dir, e.Filename)
		if fi, serr := os.Stat(full); serr == nil {
			freedBytes += fi.Size()
			count++
		}
		_ = os.Remove(full)
	}
	// Orphans the manifest never knew about (a crash between
	// rename and SavePreviewEntry leaves one) go too.
	if files, rerr := os.ReadDir(pc.dir); rerr == nil {
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			full := filepath.Join(pc.dir, f.Name())
			if fi, serr := os.Stat(full); serr == nil {
				freedBytes += fi.Size()
			}
			_ = os.Remove(full)
		}
	}
	if err := pc.db.DeleteAllPreviewEntries(); err != nil {
		return freedBytes, count, err
	}
	return freedBytes, count, nil
}

// PreviewCacheInfo is the single return value of
// PreviewCacheInfo: current size + file count + the configured
// limit, for Settings → 缓存.
type PreviewCacheInfo struct {
	SizeBytes int64 `json:"sizeBytes"`
	FileCount int   `json:"fileCount"`
	LimitMB   int   `json:"limitMB"`
}

// PreviewCacheInfo reports the cache as it stands on disk.
func (a *App) PreviewCacheInfo() (*PreviewCacheInfo, error) {
	pc, err := a.previewCacheManager()
	if err != nil {
		return nil, err
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	size, count, err := pc.infoLocked()
	if err != nil {
		return nil, err
	}
	return &PreviewCacheInfo{SizeBytes: size, FileCount: count, LimitMB: a.previewCacheLimitMB()}, nil
}

// PreviewCacheCleared is the single return value of
// ClearPreviewCache: how much "清理缓存" freed.
type PreviewCacheCleared struct {
	FreedBytes int64 `json:"freedBytes"`
	FileCount  int   `json:"fileCount"`
}

// ClearPreviewCache deletes every cached preview copy and the
// manifest. The cloud originals are untouched, so nothing is
// lost — no second confirmation by design.
func (a *App) ClearPreviewCache() (*PreviewCacheCleared, error) {
	pc, err := a.previewCacheManager()
	if err != nil {
		return nil, err
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	freed, count, err := pc.clearLocked()
	if err != nil {
		return nil, err
	}
	return &PreviewCacheCleared{FreedBytes: freed, FileCount: count}, nil
}

// ClearPreviewCacheOnExit clears the cache when the
// "退出时清理缓存" setting is on. Called from main after the
// window loop returns (mygo has no OnExit hook). When the setting
// is off it is a no-op; when the cache is unavailable it is a
// plain error for the caller to log, never fatal.
func (a *App) ClearPreviewCacheOnExit() error {
	s, err := a.GetSettings()
	if err != nil {
		return err
	}
	if !s.PreviewCacheClearOnExit {
		return nil
	}
	_, err = a.ClearPreviewCache()
	return err
}

// previewCacheEnsure resolves a file and returns its cached local
// path, downloading it on a miss. Used by "系统应用打开" and
// "在 Finder 中显示".
func (a *App) previewCacheEnsure(ctx context.Context, mountID, ref string) (string, error) {
	h, ok := a.Mount(mountID)
	if !ok {
		return "", fmt.Errorf("app: 未知的网盘 %q", mountID)
	}
	st, err := h.Provider.Stat(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("app: 预览失败：%w", err)
	}
	if st.IsDir {
		return "", fmt.Errorf("app: 文件夹不能用系统应用打开")
	}
	pc, err := a.previewCacheManager()
	if err != nil {
		return "", err
	}
	path, _, err := pc.ensure(ctx, h.Provider, a.transferLimiter, mountID, ref, st.Size, st.ModTime)
	if err != nil {
		return "", err
	}
	// A download may have pushed the cache over the limit:
	// trim now (the file just written was accessed just now,
	// so the recent guard never evicts it).
	if err := func() error {
		pc.mu.Lock()
		defer pc.mu.Unlock()
		return pc.trimLocked(a.previewCacheLimitMB())
	}(); err != nil {
		return "", err
	}
	return path, nil
}
