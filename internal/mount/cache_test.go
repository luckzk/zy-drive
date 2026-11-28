// SPDX-License-Identifier: MPL-2.0
package mount

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/cache"
)

// TestCacheEnsureDownloadsAndReuses: first open downloads the
// whole file, the second reuses the cache (no second download).
func TestCacheEnsureDownloadsAndReuses(t *testing.T) {
	env := newTestEnv(t, 5)
	p := env.providers["drive-1"]
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	p.putFile("/notes.txt", "hello", mtime)
	ctx := context.Background()
	path, reused, err := env.svc.cache.ensure(ctx, p, nil, "drive-1", "/notes.txt", 5, mtime)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if reused {
		t.Fatalf("first ensure should download, not reuse")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "hello" {
		t.Fatalf("cached content = %q, err %v", data, err)
	}
	calls := p.listCalls
	if calls != 0 {
		t.Fatalf("listCalls = %d, want 0 (ensure must not list)", calls)
	}
	path2, reused, err := env.svc.cache.ensure(ctx, p, nil, "drive-1", "/notes.txt", 5, mtime)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if !reused || path2 != path {
		t.Fatalf("second ensure should reuse the cache: reused=%v path2=%q", reused, path2)
	}
}

// TestCacheEnsureOversizedRefused: a file bigger than the whole
// cache cap is refused with ErrNoSpace before any download.
func TestCacheEnsureOversizedRefused(t *testing.T) {
	env := newTestEnv(t, 2) // 2 GB cap
	p := env.providers["drive-1"]
	// A 3 GB file can never fit a 2 GB cache.
	_, _, err := env.svc.cache.ensure(context.Background(), p, nil, "drive-1", "/huge.bin", 3<<30, time.Now())
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("oversized ensure should fail with ErrNoSpace, got %v", err)
	}
	if p.openReadCalls != 0 {
		t.Fatalf("oversized ensure downloaded %d times, want 0 (refuse before any download)", p.openReadCalls)
	}
}

// TestCacheClearKeepsDirty: "清理缓存" frees clean files only —
// dirty files (un-uploaded user data) stay.
func TestCacheClearKeepsDirty(t *testing.T) {
	env := newTestEnv(t, 5)
	p := env.providers["drive-1"]
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	p.putFile("/a.txt", "aaa", mtime)
	p.putFile("/b.txt", "bbb", mtime)
	ctx := context.Background()
	if _, _, err := env.svc.cache.ensure(ctx, p, nil, "drive-1", "/a.txt", 3, mtime); err != nil {
		t.Fatalf("ensure a: %v", err)
	}
	if _, _, err := env.svc.cache.ensure(ctx, p, nil, "drive-1", "/b.txt", 3, mtime); err != nil {
		t.Fatalf("ensure b: %v", err)
	}
	// Edit b.txt locally: rewrite the cached copy, mark dirty.
	full := env.svc.cache.pathFor("drive-1", "/b.txt")
	if err := os.WriteFile(full, []byte("bbb-edited"), 0o644); err != nil {
		t.Fatalf("edit b: %v", err)
	}
	if err := env.svc.cache.markDirty("drive-1", "/b.txt"); err != nil {
		t.Fatalf("markDirty: %v", err)
	}
	cleared, err := env.svc.cache.clear()
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.FileCount != 1 || cleared.FreedBytes != 3 {
		t.Fatalf("clear freed %+v, want 1 file / 3 bytes (only a.txt)", cleared)
	}
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("dirty b.txt should survive clear: %v", err)
	}
	if _, err := os.Stat(env.svc.cache.pathFor("drive-1", "/a.txt")); !os.IsNotExist(err) {
		t.Fatalf("clean a.txt should be gone after clear")
	}
}

// TestCacheReconcileDropsOrphans: a manifest row without a file
// is dropped, a file without a row is deleted.
func TestCacheReconcileDropsOrphans(t *testing.T) {
	env := newTestEnv(t, 5)
	p := env.providers["drive-1"]
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	p.putFile("/a.txt", "aaa", mtime)
	if _, _, err := env.svc.cache.ensure(context.Background(), p, nil, "drive-1", "/a.txt", 3, mtime); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// Orphan file on disk, no manifest row.
	orphan := filepath.Join(env.svc.cache.dir, "orphan.txt")
	if err := os.WriteFile(orphan, []byte("x"), 0o644); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	// Manifest row whose file is gone.
	if err := os.Remove(env.svc.cache.pathFor("drive-1", "/a.txt")); err != nil {
		t.Fatalf("remove cached file: %v", err)
	}
	if err := env.svc.cache.reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan file should be deleted by reconcile")
	}
	if e, err := env.svc.cache.entry("drive-1", "/a.txt"); err != nil || e != nil {
		t.Fatalf("row without a file should be dropped, got %+v, err %v", e, err)
	}
}

// TestFileNodeOpenOversized: opening a file bigger than the
// cache's free space fails with ENOSPC at the FUSE layer.
func TestFileNodeOpenOversized(t *testing.T) {
	env := newTestEnv(t, 2) // 2 GB cap
	p := env.providers["drive-1"]
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	p.putFile("/huge.bin", "x", mtime)
	// The remote claims 3 GB (the local bytes are small, but
	// the open path must trust the remote size).
	p.sizeOverride["/huge.bin"] = 3 << 30
	node := &fileNode{svc: env.svc, mountID: "drive-1", ref: "/huge.bin"}
	_, _, errno := node.Open(context.Background(), 0) // O_RDONLY
	if errno != syscall.ENOSPC {
		t.Fatalf("open of an oversized file should fail with ENOSPC, got %v", errno)
	}
	if p.openReadCalls != 0 {
		t.Fatalf("oversized open downloaded %d times, want 0 (refuse before any download)", p.openReadCalls)
	}
}

// TestMountCacheManifestRoundTrip covers the v26 manifest CRUD
// (the cache package's own table).
func TestMountCacheManifestRoundTrip(t *testing.T) {
	db, err := cache.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer db.Close()
	v, err := db.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 29 {
		t.Fatalf("schema version = %d, want 29 (v26 mount_cache + v27 remote_size + v28 delete_enabled + v29 deferred_deletes)", v)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.SaveMountCacheEntry(&cache.MountCacheEntry{
		Key: "k1", MountID: "m1", Ref: "/a.txt", Filename: "k1_a.txt",
		Size: 3, RemoteMtime: now, LocalMtime: now, LastAccess: now,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	e, err := db.LoadMountCacheEntry("k1")
	if err != nil || e == nil || e.Size != 3 || e.Dirty {
		t.Fatalf("load = %+v, err %v", e, err)
	}
	if err := db.DeleteMountCacheEntry("k1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if e, err := db.LoadMountCacheEntry("k1"); err != nil || e != nil {
		t.Fatalf("deleted row should be gone, got %+v, err %v", e, err)
	}
}
