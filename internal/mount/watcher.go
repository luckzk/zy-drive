// SPDX-License-Identifier: MPL-2.0
package mount

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// watcher.go — the per-mount change watcher (v0.19.0).
//
// go-fuse on macOS has no NOTIFY, so the kernel never learns
// about remote changes on its own: one goroutine per enabled
// mount polls for them and invalidates the affected directory
// listings + stale cached files. A file that is open right now
// is never swapped under its editor — its cached copy is left
// alone and the editor discovers the change itself.
//
// P0 mounts (Drive, Dropbox, OneDrive) all implement
// storage.DeltaSource (changes.list / longpoll / delta), polled
// every 60s from a cursor that starts at "now" (no history
// replay on start).
//
// S3 / Drime have no delta: their discovery is a timed full
// List comparison of visited directories only. That logic is
// written here (pollVisitedDirs) but NOT enabled in P0 —
// pollingEnabled is false and the watcher for a non-delta mount
// simply exits. P0 only mounts the three delta drives, so
// nothing polls in P0.

// deltaPollInterval is the DeltaSource poll cadence. A var, not
// a const, so tests can shrink it without waiting out the real
// 60s.
var deltaPollInterval = 60 * time.Second

// pollingEnabled gates the S3/Drime visited-directory poll.
// P0: false (only delta drives are mountable).
const pollingEnabled = false

// pollInterval is the S3/Drime visited-directory poll cadence
// (user decision, 2026-11-27): 5 minutes, visited dirs only.
// A var so tests can shrink it.
var pollInterval = 5 * time.Minute

type watcher struct {
	svc     *Service
	mountID string
	stopCh  chan struct{}
	once    sync.Once
}

func newWatcher(svc *Service, mountID string) *watcher {
	return &watcher{svc: svc, mountID: mountID, stopCh: make(chan struct{})}
}

func (w *watcher) stop() {
	w.once.Do(func() { close(w.stopCh) })
}

// run polls until stopped. A failed poll never kills the
// watcher: it logs once and backs off to the next tick — a
// transient network blip must not wedge change discovery.
func (w *watcher) run() {
	prov, err := w.svc.providerFor(w.mountID)
	if err != nil {
		log.Printf("mount: watcher %s: no provider: %v", w.mountID, err)
		return
	}
	delta, hasDelta := prov.(storage.DeltaSource)
	if !hasDelta {
		if !pollingEnabled {
			// P0: a non-delta mount has no discovery yet.
			return
		}
		w.runPoll()
		return
	}
	cursor := ""
	ticker := time.NewTicker(deltaPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		changes, newCursor, err := delta.Delta(ctx, cursor)
		cancel()
		if err != nil {
			log.Printf("mount: watcher %s: delta: %v", w.mountID, err)
			continue
		}
		cursor = newCursor
		w.applyChanges(changes)
	}
}

// runPoll is the S3/Drime visited-directory poll (written, P0
// disabled): every pollInterval, re-List each directory this
// mount has actually visited and invalidate the ones that
// changed. Never a full-volume scan.
func (w *watcher) runPoll() {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
		}
		w.pollVisitedDirs()
	}
}

// pollVisitedDirs re-lists visited directories and invalidates
// the ones whose contents changed.
func (w *watcher) pollVisitedDirs() {
	w.svc.mu.Lock()
	var dirs []string
	for key := range w.svc.listings {
		if strings.HasPrefix(key, w.mountID+"\x00") {
			dirs = append(dirs, strings.TrimPrefix(key, w.mountID+"\x00"))
		}
	}
	w.svc.mu.Unlock()
	for _, dir := range dirs {
		select {
		case <-w.stopCh:
			return
		default:
		}
		w.svc.mu.Lock()
		before := w.svc.listings[w.mountID+"\x00"+dir]
		w.svc.mu.Unlock()
		prov, err := w.svc.providerFor(w.mountID)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		after, err := prov.List(ctx, dir)
		cancel()
		if err != nil {
			// One failed poll stops this round (Drime 429
			// caution): report once, do not loop-retry.
			log.Printf("mount: watcher %s: poll %q: %v", w.mountID, dir, err)
			return
		}
		if before == nil || dirChanged(before.entries, after) {
			w.svc.invalidateDir(w.mountID, dir)
			for _, n := range after {
				w.dropStaleCache(n)
			}
		}
	}
}

// dirChanged compares two listings by ref + size + mtime.
func dirChanged(a, b []domain.Node) bool {
	if len(a) != len(b) {
		return true
	}
	byRef := map[string]domain.Node{}
	for _, n := range a {
		byRef[n.Ref] = n
	}
	for _, n := range b {
		old, ok := byRef[n.Ref]
		if !ok {
			return true
		}
		if old.Size != n.Size || !old.ModTime.Equal(n.ModTime) {
			return true
		}
	}
	return false
}

// applyChanges invalidates the parents of changed nodes and
// drops their stale cached copies (unless dirty or open).
func (w *watcher) applyChanges(changes []domain.Node) {
	for _, n := range changes {
		w.svc.invalidateDir(w.mountID, parentRef(n.Ref))
		w.dropStaleCache(n)
	}
}

// dropStaleCache deletes one file's cached copy when the remote
// changed — unless the copy is dirty (user's un-uploaded work)
// or open (an editor has it).
func (w *watcher) dropStaleCache(n domain.Node) {
	if n.IsDir {
		return
	}
	e, err := w.svc.cache.entry(w.mountID, n.Ref)
	if err != nil || e == nil || e.Dirty {
		return
	}
	w.svc.cache.mu.Lock()
	open := w.svc.cache.isOpenLocked(w.mountID, n.Ref)
	w.svc.cache.mu.Unlock()
	if open {
		return
	}
	if e.Size == n.Size && e.RemoteMtime.Equal(n.ModTime) {
		return // unchanged: keep the cache
	}
	_ = w.svc.cache.deleteEntry(w.mountID, n.Ref)
}
