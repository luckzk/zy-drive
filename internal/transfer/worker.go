// SPDX-License-Identifier: MPL-2.0
package transfer

import (
	"context"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// Worker executes queued transfers with a bounded concurrency and an
// optional per-transfer speed limit. It knows nothing about sync — it
// only moves bytes. The actual move is done by Executor, injected by
// the app layer (which knows the providers involved).
//
// Concurrency and the limiter are live settings (V0.9.0): the
// Settings page changes them and ApplyTransferSettings pushes the
// new values here — in-flight transfers keep running, the next
// acquire honours the new cap, and the limiter paces the next read
// (the executor reads it via Limiter()).
type Executor func(ctx context.Context, t *domain.Transfer, report func(done, speedBps int64)) error

// Worker runs the queue.
type Worker struct {
	queue    *Queue
	executor Executor
	sem      *Semaphore

	mu      sync.Mutex
	limiter *Limiter
	running map[string]context.CancelFunc
}

// NewWorker returns a Worker. concurrency defaults to 2 (conservative,
// matching Air Explorer's free-tier behaviour); limiter may be nil.
func NewWorker(q *Queue, exec Executor, concurrency int, limiter *Limiter) *Worker {
	if concurrency <= 0 {
		concurrency = 2
	}
	return &Worker{queue: q, executor: exec, sem: NewSemaphore(concurrency),
		limiter: limiter, running: map[string]context.CancelFunc{}}
}

// SetConcurrency changes the parallel-transfer cap live (clamped
// 1..16). Already-running transfers are not interrupted.
func (w *Worker) SetConcurrency(n int) {
	if n < 1 {
		n = 1
	}
	if n > 16 {
		n = 16
	}
	w.sem.SetLimit(n)
}

// Concurrency returns the current cap.
func (w *Worker) Concurrency() int { return w.sem.Limit() }

// SetLimiter swaps the rate limiter live (nil = unlimited).
func (w *Worker) SetLimiter(l *Limiter) {
	w.mu.Lock()
	w.limiter = l
	w.mu.Unlock()
}

// Limiter returns the current rate limiter (may be nil).
func (w *Worker) Limiter() *Limiter {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.limiter
}

// Start launches the worker loop until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// Pick the next queued transfer.
		var next *domain.Transfer
		for _, t := range w.queue.List() {
			if t.Status == domain.TransferQueued {
				next = t
				break
			}
		}
		if next == nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if !w.sem.Acquire(ctx) {
			return
		}
		go func(t *domain.Transfer) {
			defer w.sem.Release()
			w.runOne(ctx, t)
		}(next)
	}
}

func (w *Worker) runOne(ctx context.Context, t *domain.Transfer) {
	tctx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.running[t.ID] = cancel
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.running, t.ID)
		w.mu.Unlock()
	}()
	_ = w.queue.SetStatus(t.ID, domain.TransferRunning, "")
	start := time.Now()
	err := w.executor(tctx, t, func(done, speedBps int64) {
		w.queue.RecordProgress(t.ID, done, speedBps)
	})
	_ = start
	if err != nil {
		if ctx.Err() != nil || tctx.Err() != nil {
			_ = w.queue.SetStatus(t.ID, domain.TransferCancelled, "cancelled")
			return
		}
		_ = w.queue.SetStatus(t.ID, domain.TransferFailed, err.Error())
		return
	}
	_ = w.queue.SetStatus(t.ID, domain.TransferCompleted, "")
}

// Cancel cancels a running transfer by ID.
func (w *Worker) Cancel(id string) {
	w.mu.Lock()
	cancel, ok := w.running[id]
	w.mu.Unlock()
	if ok {
		cancel()
	}
}
