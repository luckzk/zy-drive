// SPDX-License-Identifier: MPL-2.0
package mount

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	"golang.org/x/sys/unix"
)

// cache.go — the mount cache (v0.19.0).
//
// A writable working set under ~/Library/Caches/
// com.luckzk.zy-drive/mount (macOS) — a sibling of the preview
// cache's …/preview, never the same directory or table. The
// preview cache is a read-only one-shot copy (0444); this one is
// the files the user is editing, with a dirty bit.
//
// Rules (design §8, user decisions 2026-11-27):
//   - Key: sha256(mount_id + "\x00" + ref); filename keeps the
//     original name (sanitized) after the key, ≤200 chars —
//     the same algorithm as the preview cache, reimplemented
//     here (the preview helpers live in internal/app and this
//     package must not import it).
//   - Reuse: size + remote mtime match and the local file
//     exists. A dirty copy is never re-downloaded over.
//   - Atomic writes: temp file + rename.
//   - LRU eviction with a 5-minute recent guard; dirty, pinned
//     and currently-open files are never evicted. Over the
//     limit, a new download is refused with ENOSPC + a human
//     reason — never a silent streaming fallback (P0 has none).
//   - "清理缓存" only clears non-dirty, non-pinned files and
//     reports the freed bytes; dirty files are un-uploaded
//     user data and stay.

// ErrNoSpace is returned by Ensure when a file cannot fit in the
// cache: either it is bigger than the whole cap, or the cap is
// full of dirty/open/recent files. The FUSE layer maps it to
// ENOSPC; the text is the human reason for logs and status.
var ErrNoSpace = errors.New("mount: 文件大于挂载缓存可用空间，请调大缓存上限（设置 → 挂载）或先清理缓存")

// mountCacheDir returns the mount cache directory for this
// platform (same UserCacheDir convention as the preview cache).
func mountCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", fmt.Errorf("mount: 找不到缓存目录：%w", err)
		}
		base = filepath.Join(home, ".cache")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(base, "com.luckzk.zy-drive", "mount"), nil
	}
	return filepath.Join(base, "zy-drive", "mount"), nil
}

// mountCacheKey is sha256(mountID + "\x00" + ref), hex.
func mountCacheKey(mountID, ref string) string {
	sum := sha256.Sum256([]byte(mountID + "\x00" + ref))
	return hex.EncodeToString(sum[:])
}

// mountCacheFilename keeps the original name (so the OS picks
// the right app) after the key. Unsafe characters become '_';
// the whole name is capped so key + name never exceeds the
// filesystem's filename limit.
func mountCacheFilename(key, name string) string {
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

// inflightCall dedupes concurrent downloads of the same file:
// the first caller downloads, the rest wait for its result.
type inflightCall struct {
	done chan struct{}
	path string
	err  error
}

// mountCache is one Service's cache. Methods are safe for
// concurrent use.
type mountCache struct {
	mu       sync.Mutex
	dir      string
	db       *cache.DB
	limitGB  func() int
	open     map[string]int // mountID+"\x00"+ref -> open handle count
	inflight map[string]*inflightCall
	// hashes notes the content hash seen at download time
	// (in-memory only): the uploader compares it before
	// overwriting, when the driver offers the same kind.
	hashes map[string]hashNote
}

type hashNote struct {
	hash string
	kind string
}

// noteRemoteHash records a download-time content hash.
func (c *mountCache) noteRemoteHash(mountID, ref, hash, kind string) {
	if hash == "" || kind == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hashes == nil {
		c.hashes = map[string]hashNote{}
	}
	c.hashes[mountID+"\x00"+ref] = hashNote{hash: hash, kind: kind}
}

// remoteHash returns the noted download-time hash, if any.
func (c *mountCache) remoteHash(mountID, ref string) (hash, kind string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.hashes[mountID+"\x00"+ref]
	if !ok {
		return "", "", false
	}
	return n.hash, n.kind, true
}

// saveEntry persists one manifest row.
func (c *mountCache) saveEntry(e *cache.MountCacheEntry) error {
	return c.db.SaveMountCacheEntry(e)
}

func newMountCache(db *cache.DB, limitGB func() int) *mountCache {
	dir, err := mountCacheDir()
	if err != nil {
		// Fall back to a temp dir so a cache failure never
		// wedges the service; the caller logs the real error
		// via info().
		dir = filepath.Join(os.TempDir(), "zy-drive-mount")
	}
	_ = os.MkdirAll(dir, 0o700)
	return &mountCache{
		dir:      dir,
		db:       db,
		limitGB:  limitGB,
		open:     map[string]int{},
		inflight: map[string]*inflightCall{},
	}
}

// limitBytes reads the current cap from Settings.
func (c *mountCache) limitBytes() int64 {
	gb := DefaultCacheLimitGB
	if c.limitGB != nil {
		if n := c.limitGB(); n > 0 {
			gb = n
		}
	}
	return int64(gb) << 30
}

// pathFor returns the local path for a (mountID, ref) pair.
func (c *mountCache) pathFor(mountID, ref string) string {
	return filepath.Join(c.dir, mountCacheFilename(mountCacheKey(mountID, ref), ref))
}

// isOpenLocked reports whether a file has open handles. Caller
// holds c.mu.
func (c *mountCache) isOpenLocked(mountID, ref string) bool {
	return c.open[mountID+"\x00"+ref] > 0
}

// AddOpen / RemoveOpen track open handles (P0 implicit pinning:
// an open file is never evicted).
func (c *mountCache) AddOpen(mountID, ref string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.open[mountID+"\x00"+ref]++
}

func (c *mountCache) RemoveOpen(mountID, ref string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := mountID + "\x00" + ref
	if c.open[k] > 1 {
		c.open[k]--
	} else {
		delete(c.open, k)
	}
}

// openCountByMount returns the open-handle count per mount.
func (c *mountCache) openCountByMount() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]int{}
	for k, n := range c.open {
		if i := strings.Index(k, "\x00"); i >= 0 {
			out[k[:i]] += n
		}
	}
	return out
}

// openTotal returns the total open-handle count.
func (c *mountCache) openTotal() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, n := range c.open {
		total += n
	}
	return total
}

// usedLocked sums the sizes of manifest rows whose file exists.
// Caller holds c.mu.
func (c *mountCache) usedLocked() int64 {
	entries, err := c.db.ListMountCacheEntries()
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if fi, err := os.Stat(filepath.Join(c.dir, e.Filename)); err == nil {
			total += fi.Size()
		}
	}
	return total
}

// trimLocked evicts least-recently-used entries until used+need
// fits the limit. Dirty, pinned, open and recently-accessed
// files are never evicted. Caller holds c.mu.
func (c *mountCache) trimLocked(need int64) error {
	limit := c.limitBytes()
	used := c.usedLocked()
	if used+need <= limit {
		return nil
	}
	entries, err := c.db.ListMountCacheEntries()
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-recentGuard)
	for _, e := range entries {
		if used+need <= limit {
			break
		}
		if e.Dirty || e.Pinned {
			continue
		}
		if c.isOpenLocked(e.MountID, e.Ref) {
			continue
		}
		if e.LastAccess.After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(c.dir, e.Filename))
		if err := c.db.DeleteMountCacheEntry(e.Key); err != nil {
			return err
		}
		used -= e.Size
	}
	return nil
}

// trimToLimit trims the cache to the current cap (Settings save
// hook, startup). Best effort: dirty/open/recent files can keep
// the cache over the cap until they are uploaded or closed.
func (c *mountCache) trimToLimit() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.trimLocked(0)
}

// ensure returns the cached local path for one remote file,
// downloading it whole (P0: Provider has no Range read) when
// the cache has no fresh copy. reused reports a cache hit.
// A file that cannot fit returns ErrNoSpace.
func (c *mountCache) ensure(ctx context.Context, provider storage.Provider, limiter *transfer.Limiter, mountID, ref string, size int64, mtime time.Time) (path string, reused bool, err error) {
	key := mountCacheKey(mountID, ref)
	full := c.pathFor(mountID, ref)

	c.mu.Lock()
	// A dirty copy is the user's un-uploaded work: never
	// re-download over it, whatever the remote says.
	// An open copy is mid-edit: never swap it under the editor.
	if e, lerr := c.db.LoadMountCacheEntry(key); lerr == nil && e != nil {
		if e.Dirty || c.isOpenLocked(mountID, ref) {
			if _, serr := os.Stat(full); serr == nil {
				now := time.Now().UTC()
				e.LastAccess = now
				_ = c.db.SaveMountCacheEntry(e)
				c.mu.Unlock()
				return full, true, nil
			}
		}
		if e.Size == size && e.RemoteMtime.Equal(mtime) {
			if fi, serr := os.Stat(full); serr == nil && fi.Size() == size {
				now := time.Now().UTC()
				e.LastAccess = now
				_ = c.db.SaveMountCacheEntry(e)
				c.mu.Unlock()
				return full, true, nil
			}
		}
	}
	// Make room first (LRU, never dirty/open/recent). A file
	// bigger than the whole cap can never fit.
	limit := c.limitBytes()
	if size > limit {
		c.mu.Unlock()
		return "", false, fmt.Errorf("%w（这个文件 %s，缓存上限 %s）", ErrNoSpace, humanBytes(size), humanBytes(limit))
	}
	if err := c.trimLocked(size); err != nil {
		c.mu.Unlock()
		return "", false, err
	}
	if c.usedLocked()+size > limit {
		c.mu.Unlock()
		return "", false, fmt.Errorf("%w（缓存已被未上传的改动占满，先等上传完成或调大上限）", ErrNoSpace)
	}
	// The disk itself must hold the file too.
	if free, ferr := c.freeBytes(); ferr == nil && free < size {
		c.mu.Unlock()
		return "", false, fmt.Errorf("%w（磁盘剩余空间不足，还剩 %s）", ErrNoSpace, humanBytes(free))
	}
	// Dedup concurrent downloads of the same file.
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.path, false, call.err
		case <-ctx.Done():
			return "", false, ctx.Err()
		}
	}
	call := &inflightCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	p, derr := c.download(ctx, provider, limiter, mountID, ref, size, mtime, full, key)
	call.path, call.err = p, derr
	close(call.done)
	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()
	if derr != nil {
		return "", false, derr
	}
	return p, false, nil
}

// download fetches one file into the cache (temp file + rename).
func (c *mountCache) download(ctx context.Context, provider storage.Provider, limiter *transfer.Limiter, mountID, ref string, size int64, mtime time.Time, full, key string) (string, error) {
	rc, err := provider.OpenRead(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("mount: 下载失败：%s", firstLine(err))
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
		return "", fmt.Errorf("mount: 写缓存失败：%w", err)
	}
	_, copyErr := io.Copy(f, body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("mount: 下载失败：%s", firstLine(copyErr))
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("mount: 写缓存失败：%w", closeErr)
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("mount: 写缓存失败：%w", err)
	}
	if err := os.Rename(tmp, full); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("mount: 写缓存失败：%w", err)
	}
	now := time.Now().UTC()
	if err := c.db.SaveMountCacheEntry(&cache.MountCacheEntry{
		Key: key, MountID: mountID, Ref: ref,
		Filename: filepath.Base(full),
		Size:     size, RemoteSize: size, RemoteMtime: mtime, LocalMtime: now,
		LastAccess: now,
	}); err != nil {
		_ = os.Remove(full)
		return "", fmt.Errorf("mount: 写缓存失败：%w", err)
	}
	return full, nil
}

// markDirty records a local change: the cached copy is now
// dirty (size + local mtime from disk) and must be uploaded.
// Called on file close after a write (or truncate).
func (c *mountCache) markDirty(mountID, ref string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := mountCacheKey(mountID, ref)
	full := c.pathFor(mountID, ref)
	fi, err := os.Stat(full)
	if err != nil {
		return fmt.Errorf("mount: 缓存文件找不到了：%w", err)
	}
	e, err := c.db.LoadMountCacheEntry(key)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if e == nil {
		// A file created in the mount (Create → close):
		// no remote version exists yet, so RemoteMtime
		// stays zero and RemoteSize stays -1 — the uploader
		// reads that as "new file, upload directly, no Stat
		// compare".
		e = &cache.MountCacheEntry{
			Key: key, MountID: mountID, Ref: ref,
			Filename:   filepath.Base(full),
			RemoteSize: -1,
		}
	}
	// Only the LOCAL size changes here: RemoteSize/RemoteMtime
	// keep describing the downloaded remote version, which is
	// what the uploader compares against.
	e.Size = fi.Size()
	e.LocalMtime = fi.ModTime().UTC()
	e.Dirty = true
	e.LastAccess = now
	return c.db.SaveMountCacheEntry(e)
}

// createEmpty creates an empty cached file for a newly created
// mount file (Create → close → Upload). The entry starts dirty
// with no remote version.
func (c *mountCache) createEmpty(mountID, ref string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	full := c.pathFor(mountID, ref)
	f, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("mount: 建缓存文件失败：%w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("mount: 建缓存文件失败：%w", err)
	}
	now := time.Now().UTC()
	if err := c.db.SaveMountCacheEntry(&cache.MountCacheEntry{
		Key: mountCacheKey(mountID, ref), MountID: mountID, Ref: ref,
		Filename: filepath.Base(full),
		Size:     0, RemoteSize: -1, Dirty: true, LastAccess: now,
	}); err != nil {
		_ = os.Remove(full)
		return "", err
	}
	return full, nil
}

// renameEntry moves one cached file to a new ref (FUSE rename):
// the cache file follows, the dirty bit follows, the manifest
// row is re-keyed. Open-handle counts follow too.
func (c *mountCache) renameEntry(mountID, oldRef, newRef string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	oldKey := mountCacheKey(mountID, oldRef)
	e, err := c.db.LoadMountCacheEntry(oldKey)
	if err != nil {
		return err
	}
	if e == nil {
		return nil // never cached: nothing to move
	}
	newKey := mountCacheKey(mountID, newRef)
	if newKey == oldKey {
		return nil
	}
	oldFull := filepath.Join(c.dir, e.Filename)
	newFull := c.pathFor(mountID, newRef)
	if _, err := os.Stat(oldFull); err == nil {
		_ = os.Remove(newFull) // a stale copy at the target
		if err := os.Rename(oldFull, newFull); err != nil {
			return fmt.Errorf("mount: 缓存改名失败：%w", err)
		}
	}
	if n := c.open[mountID+"\x00"+oldRef]; n > 0 {
		c.open[mountID+"\x00"+newRef] += n
		delete(c.open, mountID+"\x00"+oldRef)
	}
	if err := c.db.DeleteMountCacheEntry(oldKey); err != nil {
		return err
	}
	e.Key = newKey
	e.Ref = newRef
	e.Filename = filepath.Base(newFull)
	e.LastAccess = time.Now().UTC()
	return c.db.SaveMountCacheEntry(e)
}

// renamePrefix moves every cached file under a renamed
// directory (dir rename: all children change ref prefix).
func (c *mountCache) renamePrefix(mountID, oldDir, newDir string) error {
	entries, err := c.db.ListMountCacheEntries()
	if err != nil {
		return err
	}
	prefix := strings.TrimSuffix(oldDir, "/") + "/"
	for _, e := range entries {
		if e.MountID != mountID {
			continue
		}
		if e.Ref != oldDir && !strings.HasPrefix(e.Ref, prefix) {
			continue
		}
		newRef := newDir + strings.TrimPrefix(e.Ref, oldDir)
		if err := c.renameEntry(mountID, e.Ref, newRef); err != nil {
			return err
		}
	}
	return nil
}

// deleteEntry drops one cached file and its manifest row (FUSE
// delete / rename-over). Open handles keep their fd; the
// manifest row is gone either way.
func (c *mountCache) deleteEntry(mountID, ref string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := mountCacheKey(mountID, ref)
	e, err := c.db.LoadMountCacheEntry(key)
	if err != nil {
		return err
	}
	if e != nil {
		_ = os.Remove(filepath.Join(c.dir, e.Filename))
	}
	return c.db.DeleteMountCacheEntry(key)
}

// entry returns the manifest row for a file, or nil.
func (c *mountCache) entry(mountID, ref string) (*cache.MountCacheEntry, error) {
	return c.db.LoadMountCacheEntry(mountCacheKey(mountID, ref))
}

// dirtyTotals counts dirty files and their bytes (exit dialog).
func (c *mountCache) dirtyTotals() (count int, bytes int64) {
	entries, err := c.db.ListMountCacheEntries()
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if !e.Dirty {
			continue
		}
		count++
		if fi, err := os.Stat(filepath.Join(c.dir, e.Filename)); err == nil {
			bytes += fi.Size()
		} else {
			bytes += e.Size
		}
	}
	return count, bytes
}

// info sums the cache as it stands on disk.
func (c *mountCache) info() (CacheInfo, error) {
	entries, err := c.db.ListMountCacheEntries()
	if err != nil {
		return CacheInfo{}, err
	}
	var size int64
	count := 0
	for _, e := range entries {
		fi, err := os.Stat(filepath.Join(c.dir, e.Filename))
		if err != nil {
			continue
		}
		size += fi.Size()
		count++
	}
	dirtyCount, dirtyBytes := c.dirtyTotals()
	limitGB := DefaultCacheLimitGB
	if c.limitGB != nil {
		if n := c.limitGB(); n > 0 {
			limitGB = n
		}
	}
	return CacheInfo{
		SizeBytes: size, FileCount: count, LimitGB: limitGB,
		DirtyCount: dirtyCount, DirtyBytes: dirtyBytes, Dir: c.dir,
	}, nil
}

// clear deletes every cached file that is neither dirty nor
// pinned. Dirty files are un-uploaded user data and stay, so
// the freed count is honest about what actually went away.
func (c *mountCache) clear() (CacheCleared, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := c.db.ListMountCacheEntries()
	if err != nil {
		return CacheCleared{}, err
	}
	var freed int64
	count := 0
	for _, e := range entries {
		if e.Dirty || e.Pinned || c.isOpenLocked(e.MountID, e.Ref) {
			continue
		}
		full := filepath.Join(c.dir, e.Filename)
		if fi, serr := os.Stat(full); serr == nil {
			freed += fi.Size()
			count++
		}
		_ = os.Remove(full)
		if err := c.db.DeleteMountCacheEntry(e.Key); err != nil {
			return CacheCleared{FreedBytes: freed, FileCount: count}, err
		}
	}
	// Orphans the manifest never knew about (a crash between
	// rename and SaveMountCacheEntry leaves one) go too.
	if files, rerr := os.ReadDir(c.dir); rerr == nil {
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			known := false
			for _, e := range entries {
				if e.Filename == f.Name() {
					known = true
					break
				}
			}
			if known {
				continue
			}
			full := filepath.Join(c.dir, f.Name())
			if fi, serr := os.Stat(full); serr == nil {
				freed += fi.Size()
			}
			_ = os.Remove(full)
		}
	}
	return CacheCleared{FreedBytes: freed, FileCount: count}, nil
}

// reconcile drops manifest rows whose file is gone and deletes
// files (including orphan .part temps) with no row. Called once
// at startup; a stale cache must never block the app.
func (c *mountCache) reconcile() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := c.db.ListMountCacheEntries()
	if err != nil {
		return err
	}
	known := map[string]bool{}
	var firstErr error
	for _, e := range entries {
		known[e.Filename] = true
		if _, err := os.Stat(filepath.Join(c.dir, e.Filename)); err != nil {
			if err := c.db.DeleteMountCacheEntry(e.Key); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	files, err := os.ReadDir(c.dir)
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
		if err := os.Remove(filepath.Join(c.dir, f.Name())); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// freeBytes reports the free space on the cache volume.
func (c *mountCache) freeBytes() (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(c.dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// humanBytes renders a byte count for a human error message.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
