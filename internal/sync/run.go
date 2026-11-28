// SPDX-License-Identifier: MPL-2.0
package sync

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// Runner executes a confirmed Plan by decomposing it into Transfers.
// It never invents work: Execute only runs items that are in the plan.
//
// Delete enforcement (§20/§22) is re-checked here from the plan's
// actual items — the stored HasDeletes flag is NOT trusted: a stale
// plan (built before the switch was turned off) or a hand-built
// plan can carry delete items with HasDeletes=false, and the old
// comment claiming a re-check existed was wrong (there was none).
// The real check now lives in ExecuteWithPolicy: deletes with the
// switch off are refused, and a tripped gate without the user's
// explicit confirmation is refused. Callers that already applied
// the policy at preview time pass it in; Execute keeps the legacy
// signature for direct callers and assumes an already-confirmed
// plan (deletes allowed, default gate thresholds).
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

// ErrDeletesDisabled / ErrDeleteGate are the sentinel errors for
// the two engine-level delete refusals. Callers can match them
// with errors.Is; the message always carries the counts.
var (
	ErrDeletesDisabled = fmt.Errorf("sync: 删除未开启")
	ErrDeleteGate      = fmt.Errorf("sync: 删除安全闸已触发")
)

// Execute runs plan items — see ExecuteWithPolicy. It assumes the
// plan was already confirmed by the caller (deletes allowed, gate
// at the defaults); the app/agent paths use ExecuteWithPolicy with
// the real policy instead.
func (r *Runner) Execute(ctx context.Context, plan *Plan, src, dst storage.Provider, srcMount, dstMount string) (*domain.Run, error) {
	confirmed := DeletePolicy{Enabled: true, GateMaxCount: DefaultDeleteGateMaxCount, GateMaxPercent: DefaultDeleteGateMaxPercent}
	return r.ExecuteWithPolicy(ctx, plan, confirmed, src, dst, srcMount, dstMount)
}

// ExecuteWithPolicy runs plan items under the resolved delete
// policy, streaming file contents through io.Pipe so cross-cloud
// copies never touch local disk. Copies run in parallel up to the
// concurrency cap; deletions are applied last and sequentially so
// a failure mid-run leaves the target recoverable. The first copy
// error cancels the remaining copies and fails the run; items
// already finished stay finished (copies are idempotent).
//
// The delete re-check (this is the check run.go used to only claim):
// the delete count is recomputed from the plan items, not read
// from HasDeletes; if the plan contains deletes while the policy
// forbids them, or the gate tripped and the plan carries no user
// confirmation (ConfirmDeletes), the run is refused BEFORE
// anything executes — no file is copied or deleted.
func (r *Runner) ExecuteWithPolicy(ctx context.Context, plan *Plan, policy DeletePolicy, src, dst storage.Provider, srcMount, dstMount string) (*domain.Run, error) {
	policy = policy.Normalize()
	run := &domain.Run{
		ID:        fmt.Sprintf("run-%d", transfer.NowUnixNano()),
		JobID:     plan.JobID,
		Status:    domain.RunRunning,
		StartedAt: transfer.Now(),
	}
	fail := func(err error) (*domain.Run, error) {
		run.Status = domain.RunFailed
		run.Error = err.Error()
		return run, err
	}
	// Real delete re-check: recount from the items.
	deleteCount := plan.CountDeletes()
	plan.DeleteCount = deleteCount
	plan.HasDeletes = deleteCount > 0
	if deleteCount > 0 && !policy.Enabled {
		return fail(fmt.Errorf("%w：计划含 %d 个删除操作，同步删除默认是关的（设置 → 同步默认，或给这个任务单独开删除开关）", ErrDeletesDisabled, deleteCount))
	}
	if deleteCount > 0 {
		if tripped, reason := policy.GateTripped(deleteCount, plan.TargetFileCount); tripped {
			plan.GateTriggered = true
			plan.GateReason = reason
			if !plan.ConfirmDeletes {
				return fail(fmt.Errorf("%w：%s，请在删除确认里选“确认删除并执行”或“只同步非删除部分”", ErrDeleteGate, reason))
			}
		} else {
			plan.GateTriggered = false
			plan.GateReason = ""
		}
	}
	if err := validateConflictChoices(plan); err != nil {
		return fail(err)
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
		return r.copyFile(ctx, item.Path, item.Path, item.Size, src, dst, run)
	case domain.ActionConflict:
		return r.executeConflict(ctx, plan, item, src, dst, run)
	default:
		return fmt.Errorf("sync: unknown plan action %q for %s", item.Action, item.Path)
	}
}

// copyFile streams one file from src to dst under dstPath (same
// content, possibly a different name — the conflict-copy case).
func (r *Runner) copyFile(ctx context.Context, srcPath, dstPath string, size int64, src, dst storage.Provider, run *domain.Run) error {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		rc, err := src.OpenRead(ctx, srcPath)
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
	if _, err := dst.Upload(ctx, dstPath, pr, size); err != nil {
		return fmt.Errorf("sync: copy %s: %w", srcPath, err)
	}
	if err := <-errCh; err != nil {
		return fmt.Errorf("sync: read %s: %w", srcPath, err)
	}
	if r.Transfers != nil {
		r.Transfers.RecordCompleted(run.ID, dstPath, size)
	}
	return nil
}

// executeConflict applies the user's per-item choice (§7). The
// runner only ever moves bytes source → target (the copy
// direction Execute supports); the target side's own version is
// already in place, so:
//   - keep-both (default): the target version keeps the original
//     name; the source version is uploaded as a conflict copy
//     ("name (conflict copy from left <date>).ext"). Nothing is
//     overwritten, nothing is deleted, no timestamp decides.
//   - keep-left: the source version overwrites the target copy
//     under the original name (explicit user choice).
//   - keep-right: nothing to do — the target already holds it.
//   - keep-newer: only when both sides are cloud drives; the
//     newer ModTime wins the name (source-newer = keep-left,
//     target-newer = keep-right). Local-involved plans are
//     rejected earlier by validateConflictChoices.
func (r *Runner) executeConflict(ctx context.Context, plan *Plan, item PlanItem, src, dst storage.Provider, run *domain.Run) error {
	d := plan.ConflictFor(item.Path)
	choice := d.ChoiceOrDefault()
	switch choice {
	case ConflictKeepRight:
		return nil
	case ConflictKeepLeft:
		size := item.Size
		if d != nil && d.LeftSize > 0 {
			size = d.LeftSize
		}
		return r.copyFile(ctx, item.Path, item.Path, size, src, dst, run)
	case ConflictKeepNewer:
		if d == nil {
			return fmt.Errorf("sync: conflict %s: no conflict detail, cannot pick newer", item.Path)
		}
		leftMod, _ := time.Parse(time.RFC3339, d.LeftModTime)
		rightMod, _ := time.Parse(time.RFC3339, d.RightModTime)
		if !leftMod.IsZero() && !rightMod.IsZero() && rightMod.After(leftMod) {
			return nil // target (right) is newer: keep it, do nothing
		}
		size := item.Size
		if d.LeftSize > 0 {
			size = d.LeftSize
		}
		return r.copyFile(ctx, item.Path, item.Path, size, src, dst, run)
	case ConflictKeepBoth:
		fallthrough
	default:
		var mod time.Time
		if d != nil {
			mod, _ = time.Parse(time.RFC3339, d.LeftModTime)
		}
		copyName := ConflictCopyName(item.Path, "left", mod)
		size := item.Size
		if d != nil && d.LeftSize > 0 {
			size = d.LeftSize
		}
		return r.copyFile(ctx, item.Path, copyName, size, src, dst, run)
	}
}
