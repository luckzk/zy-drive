// SPDX-License-Identifier: MPL-2.0
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
	// Root kinds decide the file-root semantics. A Stat error means
	// "not provably a file": folder behaviour is kept, exactly as
	// Scan does, so a driver that cannot Stat a root changes
	// nothing.
	srcRoot, srcErr := src.Stat(ctx, job.SrcRef)
	dstRoot, dstErr := dst.Stat(ctx, job.DstRef)
	srcIsFile := srcErr == nil && !srcRoot.IsDir
	dstIsFile := dstErr == nil && !dstRoot.IsDir
	if len(srcSnap.Nodes) == 0 && srcIsFile {
		// Scan seeds a file root whenever Stat sees it, so an
		// empty snapshot for a file root means the scan itself
		// could not read it — never silently plan zero items.
		return nil, fmt.Errorf("app: preview: source %q is a single file but the scan found nothing under it — it may have been deleted, or the drive could not read it; no plan was built", job.SrcRef)
	}
	if dstIsFile && !srcIsFile {
		// A whole folder cannot sync into one file.
		return nil, fmt.Errorf("app: preview: target %q is a single file but the source is a folder — pick a folder as the target, or a single file as the source", job.DstRef)
	}
	if srcIsFile && dstIsFile {
		// File-to-file: the target file receives the source file's
		// content, whatever the two files are named. Re-key the
		// target snapshot's single entry to the source file's name
		// so Compare sees one path instead of a spurious
		// upload-plus-delete pair.
		var srcKey string
		for k := range srcSnap.Nodes {
			srcKey = k
		}
		if srcKey != "" {
			for _, dn := range dstSnap.Nodes {
				nd := dn
				nd.Path = srcKey
				dstSnap.Nodes = map[string]domain.Node{srcKey: nd}
				break
			}
		}
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
// A job whose RunOn is not local is rejected here instead of
// silently running on the desktop, because the files would then
// move through the wrong machine. The Agent process runs such jobs
// through RunJobAsAgent instead.
func (a *App) RunJob(ctx context.Context, job *domain.SyncJob, plan *sync.Plan) (*domain.Run, error) {
	if err := ValidateJob(job); err != nil {
		return nil, err
	}
	if job.RunOn != domain.RunOnLocal {
		return nil, fmt.Errorf("app: job %q runs on an Agent; run it from the Agent (or push it there first)", job.Name)
	}
	return a.runJob(ctx, job, plan, true)
}

// RunJobAsAgent is the Agent process's entry point for a job stored
// in the Agent's own database. It is the same execution as RunJob
// except it accepts agent/server jobs and refuses local ones (a
// local-folder job can never run on a server: ValidateJob rejects
// it, and the Agent has no access to the desktop's disk anyway).
// The runner (internal/agentrun) persists the run record itself,
// so persist=false here — one writer per run, no duplicate rows.
func (a *App) RunJobAsAgent(ctx context.Context, job *domain.SyncJob, plan *sync.Plan) (*domain.Run, error) {
	if err := ValidateJob(job); err != nil {
		return nil, err
	}
	if job.RunOn == domain.RunOnLocal {
		return nil, fmt.Errorf("app: job %q runs locally; it cannot run on this Agent", job.Name)
	}
	return a.runJob(ctx, job, plan, false)
}

func (a *App) runJob(ctx context.Context, job *domain.SyncJob, plan *sync.Plan, persist bool) (*domain.Run, error) {
	src, err := a.ProviderFor(job.SrcMount)
	if err != nil {
		return nil, err
	}
	dst, err := a.ProviderFor(job.DstMount)
	if err != nil {
		return nil, err
	}
	conc, lim := a.transferEngineSettings()
	// Plan item paths are relative to each side's sync root (see
	// sync.Scan); re-attach the roots so execution touches the
	// right nodes — a job syncing /photos -> /backup reads
	// "/photos/a.txt" and writes "/backup/a.txt", while a job whose
	// root is a single file reads/writes that file itself
	// (sync.RootedProvider picks the wrapper by Stat).
	src = sync.RootedProvider(ctx, src, job.SrcRef)
	dst = sync.RootedProvider(ctx, dst, job.DstRef)
	runner := sync.NewRunner(a.Queue, conc, lim)
	run, err := runner.Execute(ctx, plan, src, dst, job.SrcMount, job.DstMount)
	if persist {
		a.recordRun(job, plan, run)
	}
	if err != nil {
		a.Events().Publish(Event{Type: EventJobFailed, JobID: job.ID, Message: err.Error()})
		return run, err
	}
	a.Events().Publish(Event{Type: EventJobDone, JobID: job.ID})
	return run, nil
}

// recordRun persists a finished desktop run (run row + plan items)
// and updates the job's LastRunAt/LastRunOK, so the run history the
// UI shows is real data, not a live-only event. A run that is still
// executing has no row yet — the transfer queue shows its progress
// until it finishes.
func (a *App) recordRun(job *domain.SyncJob, plan *sync.Plan, run *domain.Run) {
	if a.DB == nil || run == nil {
		return
	}
	summary := plan.Summary()
	run.Plan = &summary
	_ = a.DB.SaveRun(run)
	items := make([]domain.RunItem, 0, len(plan.Items))
	for _, it := range plan.Items {
		items = append(items, domain.RunItem{
			RunID: run.ID, Path: it.Path, Action: it.Action,
			Size: it.Size, Note: it.Note, Done: run.Status == domain.RunCompleted,
		})
	}
	_ = a.DB.SaveRunItems(run.ID, items)
	now := time.Now().UTC()
	job.LastRunAt = &now
	job.LastRunOK = run.Status == domain.RunCompleted
	_ = a.DB.SaveSyncJob(job)
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
