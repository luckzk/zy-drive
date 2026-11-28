// SPDX-License-Identifier: MPL-2.0
package mount

import (
	"context"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// TestWatcherDeltaDropsStaleCache: when the delta feed reports
// a file changed, the watcher invalidates its parent listing
// and drops the stale cached copy (unless dirty or open).
func TestWatcherDeltaDropsStaleCache(t *testing.T) {
	oldInterval := deltaPollInterval
	deltaPollInterval = 50 * time.Millisecond
	defer func() { deltaPollInterval = oldInterval }()
	env := newTestEnv(t, 5)
	p := env.providers["drive-1"]
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	p.putFile("/notes.txt", "v1", mtime)
	ctx := context.Background()
	// Visit the root dir (seeds the listing) and cache the file.
	if _, err := env.svc.listDir("drive-1", ""); err != nil {
		t.Fatalf("listDir: %v", err)
	}
	if _, _, err := env.svc.cache.ensure(ctx, p, nil, "drive-1", "/notes.txt", 2, mtime); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// Enable the mount (starts the watcher), then report a
	// remote change through the delta feed.
	if err := env.svc.SetEnabled(ctx, "drive-1", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	waitFor(t, "volume mounted", func() bool { return env.svc.Status().State == StateMounted })
	p.mu.Lock()
	p.deltaChanges = []domain.Node{{
		Ref: "/notes.txt", Path: "/notes.txt", Name: "notes.txt",
		Size: 10, ModTime: time.Now().Truncate(time.Second),
	}}
	p.mu.Unlock()
	waitFor(t, "stale cache dropped", func() bool {
		e, err := env.svc.cache.entry("drive-1", "/notes.txt")
		return err == nil && e == nil
	})
	// The parent listing was invalidated too: the next listDir
	// re-lists (listCalls grows).
	before := p.listCalls
	if _, err := env.svc.listDir("drive-1", ""); err != nil {
		t.Fatalf("listDir after change: %v", err)
	}
	if p.listCalls <= before {
		t.Fatalf("listing should have been invalidated (listCalls %d, was %d)", p.listCalls, before)
	}
}

// TestWatcherKeepsDirtyCache: a dirty cached copy is never
// dropped by the watcher — it is the user's un-uploaded work.
func TestWatcherKeepsDirtyCache(t *testing.T) {
	oldInterval := deltaPollInterval
	deltaPollInterval = 50 * time.Millisecond
	defer func() { deltaPollInterval = oldInterval }()
	env := newTestEnv(t, 5)
	p := env.providers["drive-1"]
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	p.putFile("/notes.txt", "v1", mtime)
	ctx := context.Background()
	if _, _, err := env.svc.cache.ensure(ctx, p, nil, "drive-1", "/notes.txt", 2, mtime); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := env.svc.cache.markDirty("drive-1", "/notes.txt"); err != nil {
		t.Fatalf("markDirty: %v", err)
	}
	if err := env.svc.SetEnabled(ctx, "drive-1", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	waitFor(t, "volume mounted", func() bool { return env.svc.Status().State == StateMounted })
	p.mu.Lock()
	p.deltaChanges = []domain.Node{{
		Ref: "/notes.txt", Path: "/notes.txt", Name: "notes.txt",
		Size: 10, ModTime: time.Now().Truncate(time.Second),
	}}
	p.mu.Unlock()
	// Give the watcher several ticks: the dirty entry must stay.
	time.Sleep(300 * time.Millisecond)
	e, err := env.svc.cache.entry("drive-1", "/notes.txt")
	if err != nil || e == nil || !e.Dirty {
		t.Fatalf("dirty cache entry must survive the watcher, got %+v, err %v", e, err)
	}
}
