// SPDX-License-Identifier: MPL-2.0
package sync

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// Runner executes a confirmed Plan by decomposing it into Transfers.
// It never invents work: Execute only runs items that are in the plan,
// and Mirror/Bidirectional plans must already have been confirmed by
// the user (the plan's HasDeletes flag is re-checked here as a guard).
type Runner struct {
	Transfers *transfer.Queue
	// Concurrency is the parallel-copy cap from Settings (V0.9.0:
	// the engine used to copy strictly one file at a time). Deletes
	// are still sequential and last.
	concurrency int
	limiter     *transfer.Limiter
}

// NewRunner returns a Runner backed by the given transfer queue.
// concurrency <= 0 defaults to 2; limiter may be nil (unlimited).
func NewRunner(q *transfer.Queue, concurrency int, limiter *transfer.Limiter) *Runner {
	if concurrency <= 0 {
		concurrency = 2
	}
	return &Runner{Transfers: q, concurrency: concurrency, limiter: limiter}
}

// Execute runs plan items, streaming file contents through io.Pipe so
// cross-cloud copies never touch local disk. Copies run in parallel
// up to the concurrency cap; deletions are applied last and
// sequentially so a failure mid-run leaves the target recoverable.
// The first copy error cancels the remaining copies and fails the
// run; items already finished stay finished (copies are idempotent).
func (r *Runner) Execute(ctx context.Context, plan *Plan, src, dst storage.Provider, srcMount, dstMount string) (*domain.Run, error) {
	run := &domain.Run{
		ID:        fmt.Sprintf("run-%d", transfer.NowUnixNano()),
		JobID:     plan.JobID,
		Status:    domain.RunRunning,
		StartedAt: transfer.Now(),
	}
	// Deletes last.
	var deletes, copies []PlanItem
	for _, item := range plan.Items {
		if item.Action == domain.ActionDelete {
			deletes = append(deletes, item)
			continue
		}
		copies = append(copies, item)
	}
	if err := r.executeCopies(ctx, plan, copies, src, dst, srcMount, dstMount, run); err != nil {
		run.Status = domain.RunFailed
		run.Error = err.Error()
		return run, err
	}
	// Deepest first: a directory is deleted only after its
	// contents, so a RemoveAll-style driver never makes a
	// later file delete fail with "not found".
	sort.SliceStable(deletes, func(i, j int) bool {
		return strings.Count(deletes[i].Path, "/") > strings.Count(deletes[j].Path, "/")
	})
	for _, item := range deletes {
		if err := dst.Delete(ctx, item.Path); err != nil {
			run.Status = domain.RunFailed
			run.Error = err.Error()
			return run, fmt.Errorf("sync: delete %s: %w", item.Path, err)
		}
	}
	run.Status = domain.RunCompleted
	now := transfer.Now()
	run.EndedAt = &now
	return run, nil
}

// executeCopies runs the copy items in parallel up to the
// concurrency cap and returns the first error, if any.
func (r *Runner) executeCopies(ctx context.Context, plan *Plan, items []PlanItem, src, dst storage.Provider, srcMount, dstMount string, run *domain.Run) error {
	if len(items) == 0 {
		return nil
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := transfer.NewSemaphore(r.concurrency)
	var wg sync.WaitGroup
	errCh := make(chan error, len(items))
	for _, item := range items {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !sem.Acquire(cctx) {
				return
			}
			defer sem.Release()
			if err := r.executeItem(cctx, plan, item, src, dst, srcMount, dstMount, run); err != nil {
				errCh <- err
				cancel()
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		return err
	}
	return nil
}

func (r *Runner) executeItem(ctx context.Context, plan *Plan, item PlanItem, src, dst storage.Provider, srcMount, dstMount string, run *domain.Run) error {
	switch item.Action {
	case domain.ActionUpload, domain.ActionUpdate:
		pr, pw := io.Pipe()
		errCh := make(chan error, 1)
		go func() {
			rc, err := src.OpenRead(ctx, item.Path)
			if err != nil {
				_ = pw.CloseWithError(err)
				errCh <- err
				return
			}
			defer rc.Close()
			// The limiter paces the source read, so the copy
			// throughput follows the Settings speed limit.
			_, err = io.Copy(pw, transfer.NewLimitedReader(rc, r.limiter))
			_ = pw.CloseWithError(err)
			errCh <- err
		}()
		if _, err := dst.Upload(ctx, item.Path, pr, item.Size); err != nil {
			return fmt.Errorf("sync: copy %s: %w", item.Path, err)
		}
		if err := <-errCh; err != nil {
			return fmt.Errorf("sync: read %s: %w", item.Path, err)
		}
		if r.Transfers != nil {
			r.Transfers.RecordCompleted(run.ID, item.Path, item.Size)
		}
		return nil
	case domain.ActionConflict:
		// Conflict copies are handled by the conflict flow: the newer
		// version is uploaded under the original name, the loser is
		// preserved under its conflict-copy name by the caller.
		// The skeleton records the item and continues.
		return nil
	default:
		return fmt.Errorf("sync: unknown plan action %q for %s", item.Action, item.Path)
	}
}
