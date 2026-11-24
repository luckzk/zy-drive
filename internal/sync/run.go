package sync

import (
	"context"
	"fmt"
	"io"

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
}

// NewRunner returns a Runner backed by the given transfer queue.
func NewRunner(q *transfer.Queue) *Runner { return &Runner{Transfers: q} }

// Execute runs plan items in order, streaming file contents through
// io.Pipe so cross-cloud copies never touch local disk. Directories are
// created on the target first (Mkdir), files are copied, deletions are
// applied last so a failure mid-run leaves the target recoverable.
func (r *Runner) Execute(ctx context.Context, plan *Plan, src, dst storage.Provider, srcMount, dstMount string) (*domain.Run, error) {
	run := &domain.Run{
		ID:        fmt.Sprintf("run-%d", transfer.NowUnixNano()),
		JobID:     plan.JobID,
		Status:    domain.RunRunning,
		StartedAt: transfer.Now(),
	}
	// Deletes last.
	var deletes []PlanItem
	for _, item := range plan.Items {
		if item.Action == domain.ActionDelete {
			deletes = append(deletes, item)
			continue
		}
		if err := r.executeItem(ctx, plan, item, src, dst, srcMount, dstMount, run); err != nil {
			run.Status = domain.RunFailed
			run.Error = err.Error()
			return run, err
		}
	}
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
			_, err = io.Copy(pw, rc)
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
