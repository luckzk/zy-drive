package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/sync"
)

// PreviewJob scans both sides of a sync job and returns the plan for
// user confirmation. It never deletes or uploads anything.
// Mirror and bidirectional plans may contain deletions; the UI must
// show the deletion list and require explicit confirmation.
func (a *App) PreviewJob(ctx context.Context, job *domain.SyncJob) (*sync.Plan, error) {
	src, err := a.ProviderFor(job.SrcMount)
	if err != nil {
		return nil, err
	}
	dst, err := a.ProviderFor(job.DstMount)
	if err != nil {
		return nil, err
	}
	srcSnap, err := sync.Scan(ctx, src, job.SrcRef)
	if err != nil {
		return nil, fmt.Errorf("app: preview scan source: %w", err)
	}
	dstSnap, err := sync.Scan(ctx, dst, job.DstRef)
	if err != nil {
		return nil, fmt.Errorf("app: preview scan target: %w", err)
	}
	switch job.Direction {
	case domain.DirectionBidirectional:
		srcChanges := sync.Compare(srcSnap, dstSnap, job.Compare, job.Exclude)
		dstChanges := sync.Compare(dstSnap, srcSnap, job.Compare, job.Exclude)
		return sync.BuildBidirectionalPlan(job.ID, srcChanges, dstChanges), nil
	default:
		changes := sync.Compare(srcSnap, dstSnap, job.Compare, job.Exclude)
		return sync.BuildPlan(job.ID, job.Direction, changes), nil
	}
}

// RunJob executes a previously confirmed plan. The caller is
// responsible for having shown the plan to the user when the plan
// contains deletions.
//
// Agent execution is not wired yet (V0.2): a job whose RunOn is not
// local is rejected here instead of silently running on the desktop,
// because the files would then move through the wrong machine.
func (a *App) RunJob(ctx context.Context, job *domain.SyncJob, plan *sync.Plan) (*domain.Run, error) {
	if err := ValidateJob(job); err != nil {
		return nil, err
	}
	if job.RunOn != domain.RunOnLocal {
		return nil, fmt.Errorf("app: job %q runs on an Agent; agent execution lands in V0.2", job.Name)
	}
	src, err := a.ProviderFor(job.SrcMount)
	if err != nil {
		return nil, err
	}
	dst, err := a.ProviderFor(job.DstMount)
	if err != nil {
		return nil, err
	}
	runner := sync.NewRunner(a.Queue)
	run, err := runner.Execute(ctx, plan, src, dst, job.SrcMount, job.DstMount)
	if err != nil {
		a.Events().Publish(Event{Type: EventJobFailed, JobID: job.ID, Message: err.Error()})
		return run, err
	}
	a.Events().Publish(Event{Type: EventJobDone, JobID: job.ID})
	return run, nil
}

// ValidateJob checks a job configuration before it is saved. In
// particular: a bidirectional job needs a baseline (first run must be a
// Resync), and source and target must be different mounts/paths.
// Local-folder sides (mount "local", refs "local://...") may only run
// on the desktop: a server Agent has no access to this machine's disk.
func ValidateJob(job *domain.SyncJob) error {
	if job.SrcMount == "" || job.DstMount == "" {
		return fmt.Errorf("app: sync job needs source and target mounts")
	}
	if job.SrcMount == job.DstMount && job.SrcRef == job.DstRef {
		return fmt.Errorf("app: sync source and target are the same folder")
	}
	switch job.Direction {
	case domain.DirectionCopy, domain.DirectionMirror, domain.DirectionBidirectional:
	default:
		return fmt.Errorf("app: unknown sync direction %q", job.Direction)
	}
	switch job.RunOn {
	case domain.RunOnLocal, domain.RunOnServer, domain.RunOnAgent:
	default:
		return fmt.Errorf("app: unknown runOn %q", job.RunOn)
	}
	if job.SrcMount == domain.LocalMountID || job.DstMount == domain.LocalMountID {
		if job.RunOn != domain.RunOnLocal {
			return fmt.Errorf("app: a job with a local folder can only run on this machine (runOn=local), not on an Agent")
		}
		if job.SrcMount == domain.LocalMountID && !domain.IsLocalRef(job.SrcRef) {
			return fmt.Errorf("app: local source ref must start with local://")
		}
		if job.DstMount == domain.LocalMountID && !domain.IsLocalRef(job.DstRef) {
			return fmt.Errorf("app: local target ref must start with local://")
		}
	}
	if job.RunOn == domain.RunOnLocal && job.AgentID != "" {
		return fmt.Errorf("app: agentId is set but runOn is local")
	}
	return nil
}

// ListSyncJobs returns saved jobs from SQLite (creation order). Empty
// when nothing was ever saved — never demo jobs.
func (a *App) ListSyncJobs() []*domain.SyncJob {
	if a.DB == nil {
		return []*domain.SyncJob{}
	}
	jobs, err := a.DB.ListSyncJobs()
	if err != nil {
		return []*domain.SyncJob{}
	}
	return jobs
}

// SaveSyncJob validates and persists a job, assigning an ID and
// timestamps when missing. Returns the stored job.
func (a *App) SaveSyncJob(job *domain.SyncJob) (*domain.SyncJob, error) {
	if err := ValidateJob(job); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if job.ID == "" {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, fmt.Errorf("app: save sync job: %w", err)
		}
		job.ID = "job-" + hex.EncodeToString(b[:])
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	if job.Compare == "" {
		job.Compare = domain.CompareSizeTime
	}
	if a.DB != nil {
		if err := a.DB.SaveSyncJob(job); err != nil {
			return nil, err
		}
	}
	return job, nil
}

// DeleteSyncJob removes a saved job. It never touches cloud data.
func (a *App) DeleteSyncJob(id string) error {
	if a.DB != nil {
		return a.DB.DeleteSyncJob(id)
	}
	return nil
}
