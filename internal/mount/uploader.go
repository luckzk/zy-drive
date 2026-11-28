// SPDX-License-Identifier: MPL-2.0
package mount

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/storage"
	syncengine "github.com/luckzk/zy-drive/internal/sync"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// uploader.go — the write-back upload worker (v0.19.0).
//
// A file close after a write marks the cached copy dirty; this
// worker uploads dirty files in close order, through the shared
// transfer limiter. Uploads are whole-file (only Drime has a
// resumable path, and the mount cache is a plain local file, so
// P0 always re-uploads the whole file).
//
// Before uploading, the worker Stats the remote and compares
// size + mtime (and the content hash when one was noted at
// download time): unchanged → Upload; changed on another
// machine → never overwrite — the local version is uploaded as
// "name (conflict copy from this Mac <yyyy-MM-dd>).ext" (the
// sync engine's naming, internal/sync/conflict.go) and the
// user is told.
//
// A failed upload keeps its dirty bit and retries 3 times
// (backoff 1 / 5 / 15 minutes); after that the file is listed
// in the mount status ("有 N 个文件上传失败") until the user
// retries (switch off→on) or the file is fixed. Local changes
// are never discarded by a failure.

// uploadBackoff is the retry schedule after a failed upload.
var uploadBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}

// maxUploadAttempts is 1 initial try + 3 retries.
const maxUploadAttempts = 4

type uploadTask struct {
	mountID string
	ref     string
	// attempts counts finished attempts (0 = never tried).
	attempts int
	nextTry  time.Time
}

type uploader struct {
	svc *Service

	mu        sync.Mutex
	cond      *sync.Cond
	queue     []*uploadTask
	queued    map[string]bool              // mountID+"\x00"+ref -> in queue
	failed    map[string]map[string]string // mountID -> ref -> display name
	running   bool
	flushMode bool
	inFlight  int
	doneCount int
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

func newUploader(svc *Service) *uploader {
	u := &uploader{
		svc:    svc,
		queued: map[string]bool{},
		failed: map[string]map[string]string{},
		stopCh: make(chan struct{}),
	}
	u.cond = sync.NewCond(&u.mu)
	return u
}

func taskKey(mountID, ref string) string { return mountID + "\x00" + ref }

// enqueue adds a dirty file to the upload queue (close order),
// deduped: a file already queued keeps its original position.
func (u *uploader) enqueue(mountID, ref string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	key := taskKey(mountID, ref)
	if u.queued[key] {
		return
	}
	u.queued[key] = true
	u.queue = append(u.queue, &uploadTask{mountID: mountID, ref: ref})
	// A fresh close clears a previous failure record: the user
	// edited again, so this is a new attempt, not the old one.
	if m, ok := u.failed[mountID]; ok {
		delete(m, ref)
		if len(m) == 0 {
			delete(u.failed, mountID)
		}
	}
	u.cond.Broadcast()
}

// renameQueued moves a queued task to a new ref (FUSE rename:
// the tmp file an editor just renamed must not upload under
// its old tmp name).
func (u *uploader) renameQueued(mountID, oldRef, newRef string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	oldKey, newKey := taskKey(mountID, oldRef), taskKey(mountID, newRef)
	if !u.queued[oldKey] {
		return
	}
	delete(u.queued, oldKey)
	u.queued[newKey] = true
	for _, t := range u.queue {
		if t.mountID == mountID && t.ref == oldRef {
			t.ref = newRef
		}
	}
	if m, ok := u.failed[mountID]; ok {
		if name, ok := m[oldRef]; ok {
			delete(m, oldRef)
			m[newRef] = name
		}
	}
}

// failedByMount lists failed-upload display names per mount
// (for the status card).
func (u *uploader) failedByMount() map[string][]string {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := map[string][]string{}
	for mountID, m := range u.failed {
		names := make([]string, 0, len(m))
		for _, name := range m {
			names = append(names, name)
		}
		sort.Strings(names)
		out[mountID] = names
	}
	return out
}

// start launches the worker goroutine (idempotent).
func (u *uploader) start() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running {
		return
	}
	u.running = true
	u.wg.Add(1)
	go u.run()
}

// stop halts the worker (in-flight uploads run to completion
// of their current attempt; the file stays dirty and retries
// next start).
func (u *uploader) stop() {
	u.mu.Lock()
	if !u.running {
		u.mu.Unlock()
		return
	}
	u.running = false
	u.mu.Unlock()
	close(u.stopCh)
	u.cond.Broadcast()
	u.wg.Wait()
}

// run is the worker loop: earliest-due task first, in close
// order for tasks due at the same time.
func (u *uploader) run() {
	defer u.wg.Done()
	for {
		u.mu.Lock()
		for {
			if !u.running {
				u.mu.Unlock()
				return
			}
			// Find the earliest-due task (flush mode treats
			// every task as due: quitting time is retry time).
			idx := -1
			var wait time.Duration
			now := time.Now()
			for i, t := range u.queue {
				if u.flushMode || !t.nextTry.After(now) {
					idx = i
					break
				}
				if wait == 0 || t.nextTry.Sub(now) < wait {
					wait = t.nextTry.Sub(now)
				}
			}
			if idx >= 0 {
				t := u.queue[idx]
				u.queue = append(u.queue[:idx], u.queue[idx+1:]...)
				delete(u.queued, taskKey(t.mountID, t.ref))
				u.inFlight++
				u.mu.Unlock()
				u.process(t)
				u.mu.Lock()
				u.inFlight--
				u.doneCount++
				u.cond.Broadcast()
				u.mu.Unlock()
				break
			}
			if wait > 0 {
				// Wait for the next retry time, a new
				// enqueue, or a stop — without a timer
				// goroutine: poll the cond with a short
				// sleep so a stop is never missed.
				u.mu.Unlock()
				select {
				case <-u.stopCh:
					return
				case <-time.After(minDuration(wait, 500*time.Millisecond)):
				}
				u.mu.Lock()
				continue
			}
			// Queue empty: wait for work.
			u.cond.Wait()
		}
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// process uploads one dirty file: compare first, then upload
// (or upload as a conflict copy when the remote changed).
func (u *uploader) process(t *uploadTask) {
	svc := u.svc
	entry, err := svc.cache.entry(t.mountID, t.ref)
	if err != nil || entry == nil || !entry.Dirty {
		return // cleaned or deleted meanwhile: nothing to do
	}
	full := svc.cache.pathFor(t.mountID, t.ref)
	fi, err := os.Stat(full)
	if err != nil {
		u.retryOrFail(t, fmt.Errorf("mount: 缓存文件找不到了：%s", firstLine(err)))
		return
	}
	prov, err := svc.providerFor(t.mountID)
	if err != nil {
		u.retryOrFail(t, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// A file created in the mount has no remote version
	// (RemoteMtime zero): upload directly, no Stat compare.
	if entry.RemoteMtime.IsZero() {
		u.upload(ctx, t, prov, full, fi.Size(), t.ref, false)
		return
	}
	// Compare before overwriting: remote size + mtime against
	// the DOWNLOADED version (RemoteSize/RemoteMtime — never
	// against Size, which markDirty overwrote with the local
	// size), and the content hash when both sides can offer
	// one of the same kind.
	statCtx, statCancel := context.WithTimeout(context.Background(), 30*time.Second)
	remote, err := prov.Stat(statCtx, t.ref)
	statCancel()
	if err != nil {
		// Cannot verify the remote state: do not upload
		// blind over a possibly-changed file. Retry later.
		u.retryOrFail(t, fmt.Errorf("mount: 上传前核对失败，稍后重试：%s", firstLine(err)))
		return
	}
	changed := !remote.ModTime.Equal(entry.RemoteMtime)
	if entry.RemoteSize >= 0 && remote.Size != entry.RemoteSize {
		changed = true
	}
	if !changed && remote.Hash != "" && remote.HashKind != "" {
		if h, kind, ok := svc.cache.remoteHash(t.mountID, t.ref); ok && kind == remote.HashKind && h != remote.Hash {
			changed = true
		}
	}
	if changed {
		conflictRef := syncengine.ConflictCopyName(t.ref, "this Mac", time.Now())
		u.upload(ctx, t, prov, full, fi.Size(), conflictRef, true)
		return
	}
	u.upload(ctx, t, prov, full, fi.Size(), t.ref, false)
}

// upload streams one local file to targetRef. conflict marks a
// conflict-copy upload: the original remote file is untouched,
// the local version lands under the conflict name instead.
func (u *uploader) upload(ctx context.Context, t *uploadTask, prov storage.Provider, full string, size int64, targetRef string, conflict bool) {
	f, err := os.Open(full)
	if err != nil {
		u.retryOrFail(t, fmt.Errorf("mount: 缓存文件找不到了：%s", firstLine(err)))
		return
	}
	defer f.Close()
	body := io.Reader(f)
	if u.svc.limiter != nil {
		if lim := u.svc.limiter(); lim != nil {
			body = transfer.NewLimitedReader(f, lim)
		}
	}
	node, err := prov.Upload(ctx, targetRef, body, size)
	if err != nil {
		u.retryOrFail(t, fmt.Errorf("mount: 上传失败，稍后重试：%s", firstLine(err)))
		return
	}
	if conflict {
		// The user's version is safe at the conflict name.
		// Drop the local copy of the original ref so the next
		// open downloads the other machine's version, and tell
		// the user what happened (status card notice).
		_ = u.svc.cache.deleteEntry(t.mountID, t.ref)
		u.svc.removePending(t.mountID, parentRef(t.ref), baseName(t.ref))
		u.svc.invalidateDir(t.mountID, parentRef(t.ref))
		u.svc.setNotice(t.mountID, fmt.Sprintf("另一台电脑也改了 %s，你的版本已另存为 %s", baseName(t.ref), baseName(targetRef)))
		return
	}
	// Success: clear dirty and record the new remote state, so
	// the next open reuses the cache instead of re-downloading.
	entry, err := u.svc.cache.entry(t.mountID, t.ref)
	if err != nil || entry == nil {
		return
	}
	fi, _ := os.Stat(full)
	entry.Dirty = false
	if node.Size >= 0 {
		entry.Size = node.Size
		entry.RemoteSize = node.Size
	} else {
		entry.Size = size
		entry.RemoteSize = size
	}
	if !node.ModTime.IsZero() {
		entry.RemoteMtime = node.ModTime
	} else {
		entry.RemoteMtime = time.Now().UTC()
	}
	if fi != nil {
		entry.LocalMtime = fi.ModTime().UTC()
	}
	entry.LastAccess = time.Now().UTC()
	_ = u.svc.cache.saveEntry(entry)
	u.svc.removePending(t.mountID, parentRef(t.ref), baseName(t.ref))
	u.svc.invalidateDir(t.mountID, parentRef(t.ref))
}

// retryOrFail schedules the next attempt (backoff 1/5/15 min)
// or records a final failure after 3 retries.
func (u *uploader) retryOrFail(t *uploadTask, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	t.attempts++
	if t.attempts >= maxUploadAttempts {
		if u.failed[t.mountID] == nil {
			u.failed[t.mountID] = map[string]string{}
		}
		u.failed[t.mountID][t.ref] = baseName(t.ref)
		return
	}
	backoff := uploadBackoff[t.attempts-1]
	if t.attempts-1 >= len(uploadBackoff) {
		backoff = uploadBackoff[len(uploadBackoff)-1]
	}
	t.nextTry = time.Now().Add(backoff)
	key := taskKey(t.mountID, t.ref)
	u.queued[key] = true
	u.queue = append(u.queue, t)
	u.cond.Broadcast()
}

// flush uploads dirty files within timeout (exit path: 30s).
// It returns how many finished and how many are still pending.
func (u *uploader) flush(timeout time.Duration) FlushResult {
	u.mu.Lock()
	u.flushMode = true
	startDone := u.doneCount
	u.cond.Broadcast()
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.flushMode = false
		u.mu.Unlock()
	}()
	deadline := time.Now().Add(timeout)
	for {
		u.mu.Lock()
		remaining := len(u.queue) + u.inFlight
		done := u.doneCount - startDone
		u.mu.Unlock()
		if remaining == 0 {
			return FlushResult{Flushed: done, Remaining: 0}
		}
		if time.Now().After(deadline) {
			return FlushResult{Flushed: done, Remaining: remaining, TimedOut: true}
		}
		time.Sleep(100 * time.Millisecond)
	}
}
