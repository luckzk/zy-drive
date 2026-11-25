// SPDX-License-Identifier: MPL-2.0
// Package transfer implements the transfer engine: one queue for every
// move in the system (upload, download, cross-cloud copy). Sync jobs
// decompose their plans into Transfers, so pause, speed limit, retry and
// progress exist in exactly one implementation. The engine knows nothing
// about sync semantics — it only sees a list of files to move.
package transfer

import (
	"fmt"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// Now returns the current time (indirection point for tests).
func Now() time.Time { return time.Now().UTC() }

// NowUnixNano returns the current time as Unix nanoseconds.
func NowUnixNano() int64 { return time.Now().UnixNano() }

// Store persists transfer state. The SQLite implementation lives in
// internal/cache; the queue works against this interface so it can be
// tested without a database.
type Store interface {
	SaveTransfer(t *domain.Transfer) error
	LoadTransfers(status domain.TransferStatus) ([]*domain.Transfer, error)
	LoadInterrupted() ([]*domain.Transfer, error)
}

// Queue is the in-memory transfer queue backed by a persistent Store.
// State is written to the store on every transition, so a killed
// process restarts with interrupted transfers marked as such.
type Queue struct {
	mu    sync.Mutex
	store Store
	items map[string]*domain.Transfer
	// Progress callbacks, called with a copy of the transfer.
	hooks []func(domain.Transfer)
}

// NewQueue returns a Queue backed by store. store may be nil for
// in-memory-only use (tests); production always passes the SQLite store.
func NewQueue(store Store) *Queue {
	return &Queue{store: store, items: map[string]*domain.Transfer{}}
}

// OnProgress registers a progress hook (e.g. the app event bus).
func (q *Queue) OnProgress(fn func(domain.Transfer)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.hooks = append(q.hooks, fn)
}

// Enqueue adds a transfer in queued state and persists it.
func (q *Queue) Enqueue(t *domain.Transfer) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if t.ID == "" {
		t.ID = "tr-" + time.Now().Format("20060102150405.000000000")
	}
	t.Status = domain.TransferQueued
	t.CreatedAt = Now()
	t.UpdatedAt = t.CreatedAt
	q.items[t.ID] = t
	return q.persistLocked(t)
}

// Requeue moves a failed, cancelled or interrupted transfer back to
// queued, so the worker picks it up again. Completed transfers are
// never re-queued (a batch retry only re-runs what did not finish —
// "断网重传后不从头传" at batch level). Done is reset to 0: the file
// itself restarts from the beginning, because no provider offers
// ranged reads yet (see executeDownload); Retries counts the
// attempts so the UI can show "第 N 次重试".
func (q *Queue) Requeue(id string) (*domain.Transfer, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.items[id]
	if !ok {
		return nil, fmt.Errorf("transfer: unknown transfer %q", id)
	}
	switch t.Status {
	case domain.TransferFailed, domain.TransferCancelled, domain.TransferInterrupted:
	default:
		return nil, fmt.Errorf("transfer: %s is %q, only failed/cancelled/interrupted transfers can be retried", id, t.Status)
	}
	t.Status = domain.TransferQueued
	t.Done = 0
	t.Error = ""
	t.Retries++
	t.UpdatedAt = Now()
	if err := q.persistLocked(t); err != nil {
		return nil, err
	}
	cp := *t
	return &cp, nil
}

// Get returns a copy of a transfer by ID.
func (q *Queue) Get(id string) (*domain.Transfer, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.items[id]
	if !ok {
		return nil, false
	}
	cp := *t
	return &cp, true
}

// List returns copies of all queued/running transfers.
func (q *Queue) List() []*domain.Transfer {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]*domain.Transfer, 0, len(q.items))
	for _, t := range q.items {
		cp := *t
		out = append(out, &cp)
	}
	return out
}

// SetStatus transitions a transfer and persists the new state.
func (q *Queue) SetStatus(id string, status domain.TransferStatus, errMsg string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.items[id]
	if !ok {
		return nil
	}
	t.Status = status
	t.Error = errMsg
	t.UpdatedAt = Now()
	return q.persistLocked(t)
}

// RecordProgress updates bytes done and notifies hooks (throttled by
// the caller — the queue itself does not rate-limit callbacks).
func (q *Queue) RecordProgress(id string, done, speedBps int64) {
	q.mu.Lock()
	t, ok := q.items[id]
	if !ok {
		q.mu.Unlock()
		return
	}
	t.Done = done
	t.SpeedBps = speedBps
	t.UpdatedAt = Now()
	cp := *t
	hooks := append([]func(domain.Transfer){}, q.hooks...)
	q.mu.Unlock()
	for _, h := range hooks {
		h(cp)
	}
}

// RecordCompleted marks a transfer done. Used by the sync runner,
// which streams through its own pipe and reports completion per item.
func (q *Queue) RecordCompleted(runID, path string, size int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, t := range q.items {
		if t.RunID == runID && t.SrcRef == path && t.Status == domain.TransferRunning {
			t.Done = size
			t.Status = domain.TransferCompleted
			t.UpdatedAt = Now()
			_ = q.persistLocked(t)
			return
		}
	}
}

func (q *Queue) persistLocked(t *domain.Transfer) error {
	if q.store == nil {
		return nil
	}
	return q.store.SaveTransfer(t)
}

// RestoreInterrupted loads transfers that were running when the process
// died and marks them interrupted for the user to resume or cancel.
func (q *Queue) RestoreInterrupted() ([]*domain.Transfer, error) {
	if q.store == nil {
		return nil, nil
	}
	items, err := q.store.LoadInterrupted()
	if err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, t := range items {
		t.Status = domain.TransferInterrupted
		q.items[t.ID] = t
		_ = q.persistLocked(t)
	}
	return items, nil
}
