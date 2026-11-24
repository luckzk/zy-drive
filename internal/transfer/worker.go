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
type Executor func(ctx context.Context, t *domain.Transfer, report func(done, speedBps int64)) error

// Worker runs the queue.
type Worker struct {
	queue       *Queue
	executor    Executor
	concurrency int
	limiter     *Limiter

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// NewWorker returns a Worker. concurrency defaults to 2 (conservative,
// matching Air Explorer's free-tier behaviour); limiter may be nil.
func NewWorker(q *Queue, exec Executor, concurrency int, limiter *Limiter) *Worker {
	if concurrency <= 0 {
		concurrency = 2
	}
	return &Worker{queue: q, executor: exec, concurrency: concurrency,
		limiter: limiter, running: map[string]context.CancelFunc{}}
}

// Start launches the worker loop until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	sem := make(chan struct{}, w.concurrency)
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
		sem <- struct{}{}
		go func(t *domain.Transfer) {
			defer func() { <-sem }()
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
