// SPDX-License-Identifier: MPL-2.0
// Package agentrun is the Agent-side job runner: it turns stored
// sync jobs into executions on a schedule, records every run, and
// reports back to the desktop. It is deliberately separate from
// internal/app (the desktop's App) and internal/agentapi (the HTTP
// server): the runner owns the schedule, the run records and the
// retry discipline, and nothing else.
//
// Self-hosted model (docs/monetization.md): the Agent runs on the
// customer's own server. There is no central scheduler, no account
// system, no push channel — the desktop pushes job configs, the
// user imports credentials via an encrypted bundle, and the Agent
// runs on its own clock. "通知" in this version means the run
// record itself: the desktop reads it back over /api/jobs/:id/runs
// (and live over SSE), so a failed scheduled run is visible there,
// never silently dropped.
package agentrun

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/agentapi"
	"github.com/luckzk/zy-drive/internal/app"
	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/platform/scheduler"
	syncengine "github.com/luckzk/zy-drive/internal/sync"
)

// retryDelay is the single retry after a failed scheduled run:
// transient errors (network blip, cloud 5xx) often clear on their
// own. Exactly one retry, then the failure stands in the run record
// until a human looks at it.
const retryDelay = 10 * time.Minute

// Runner executes jobs stored in the Agent's database.
type Runner struct {
	app     *app.App
	db      *cache.DB
	sched   *scheduler.Scheduler
	publish func(jobID string, ev agentapi.Event)
	logf    func(format string, args ...any)

	mu      sync.Mutex
	running map[string]bool
	retries map[string]*time.Timer
}

// New returns a Runner. The scheduler must already be started by
// the caller (cmd/agent); RegisterJobs wires the stored jobs into it.
func New(a *app.App, db *cache.DB, sched *scheduler.Scheduler) *Runner {
	return &Runner{
		app: a, db: db, sched: sched,
		running: map[string]bool{},
		retries: map[string]*time.Timer{},
		logf:    log.Printf,
	}
}

// SetPublish installs the SSE broadcaster (usually Server.Publish).
func (r *Runner) SetPublish(fn func(jobID string, ev agentapi.Event)) {
	r.publish = fn
}

// SetLogger replaces the default log.Printf (tests).
func (r *Runner) SetLogger(fn func(format string, args ...any)) {
	if fn != nil {
		r.logf = fn
	}
}

func (r *Runner) emit(jobID string, ev agentapi.Event) {
	if r.publish != nil {
		r.publish(jobID, ev)
	}
}

// RegisterJobs loads the stored jobs and registers every enabled,
// scheduled one with the scheduler. Call once at startup, after
// the scheduler starts. Jobs with an invalid schedule are skipped
// with a log line (they stay stored; fixing the schedule via the
// desktop re-registers them).
func (r *Runner) RegisterJobs() error {
	jobs, err := r.db.ListSyncJobs()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		r.JobUpserted(*j)
	}
	return nil
}

// JobUpserted (re)registers one job: enabled + scheduled jobs get
// a scheduler entry; manual or disabled jobs are removed from it.
// It also refreshes the stored NextRunAt.
func (r *Runner) JobUpserted(job domain.SyncJob) {
	if !job.Enabled || job.Schedule == "" {
		r.sched.Remove(job.ID)
		r.updateNextRun(job.ID, nil)
		return
	}
	if err := scheduler.ValidateSchedule(job.Schedule); err != nil {
		r.logf("agentrun: job %q (%s) has an invalid schedule, not registered: %v", job.Name, job.ID, err)
		r.sched.Remove(job.ID)
		return
	}
	next, err := scheduler.NextRun(job.Schedule, time.Now())
	if err != nil {
		r.logf("agentrun: job %q: %v", job.Name, err)
		return
	}
	r.updateNextRun(job.ID, &next)
	j := job
	r.sched.Add(&scheduler.Job{
		ID:       j.ID,
		Schedule: j.Schedule,
		Run: func(ctx context.Context) {
			r.executeJob(ctx, j, "", false)
		},
	})
}

// JobDeleted removes a job from the scheduler and cancels any
// pending retry timer for it.
func (r *Runner) JobDeleted(jobID string) {
	r.sched.Remove(jobID)
	r.mu.Lock()
	if t, ok := r.retries[jobID]; ok {
		t.Stop()
		delete(r.retries, jobID)
	}
	r.mu.Unlock()
}

// RunNow starts an immediate run (async) and returns its run id.
// A second run while one is already executing is refused: two
// runners on the same job would copy the same files twice.
func (r *Runner) RunNow(ctx context.Context, jobID string) (string, error) {
	job, err := r.loadJob(jobID)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	if r.running[jobID] {
		r.mu.Unlock()
		return "", fmt.Errorf("agentrun: 任务 %q 正在运行，请等它跑完", job.Name)
	}
	r.mu.Unlock()
	runID := fmt.Sprintf("run-%d", time.Now().UnixNano())
	go r.executeJob(context.Background(), job, runID, false)
	return runID, nil
}

func (r *Runner) loadJob(jobID string) (domain.SyncJob, error) {
	jobs, err := r.db.ListSyncJobs()
	if err != nil {
		return domain.SyncJob{}, err
	}
	for _, j := range jobs {
		if j.ID == jobID {
			return *j, nil
		}
	}
	return domain.SyncJob{}, fmt.Errorf("agentrun: unknown job %q", jobID)
}

// executeJob runs one job end to end: preview, delete guard, run,
// record, notify. runID "" means "assign one now" (scheduled runs).
// isRetry suppresses the automatic retry (one retry only).
func (r *Runner) executeJob(ctx context.Context, job domain.SyncJob, runID string, isRetry bool) {
	// Re-load: the stored job may have changed since scheduling.
	fresh, err := r.loadJob(job.ID)
	if err != nil {
		r.logf("agentrun: job %s vanished before its run: %v", job.ID, err)
		return
	}
	job = fresh
	r.mu.Lock()
	if r.running[job.ID] {
		r.mu.Unlock()
		r.logf("agentrun: job %q is already running, skipping this trigger", job.Name)
		return
	}
	r.running[job.ID] = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, job.ID)
		r.mu.Unlock()
	}()

	if runID == "" {
		runID = fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	run := &domain.Run{
		ID: runID, JobID: job.ID, Status: domain.RunRunning,
		StartedAt: time.Now().UTC(),
	}
	_ = r.db.SaveRun(run)
	r.emit(job.ID, agentapi.Event{Type: "plan", RunID: runID, Message: "开始执行"})
	fail := func(err error, retryable bool) {
		now := time.Now().UTC()
		run.Status = domain.RunFailed
		run.Error = err.Error()
		run.EndedAt = &now
		_ = r.db.SaveRun(run)
		r.markJobRun(job, false)
		r.emit(job.ID, agentapi.Event{Type: "error", RunID: runID, Message: err.Error()})
		r.logf("agentrun: job %q failed: %v", job.Name, err)
		if retryable && !isRetry {
			r.scheduleRetry(job)
		}
	}
	plan, err := r.app.PreviewJob(ctx, &job)
	if err != nil {
		fail(fmt.Errorf("生成同步计划失败：%w", err), true)
		return
	}
	summary := plan.Summary()
	run.Plan = &summary
	// Delete handling for unattended runs (§20/§22, changed in
	// v0.20.0 — the old code failed the whole run here):
	//   - Switch off: PreviewJob already stripped the deletes
	//     (plan.DeletesDeferred, DeferredDeleteCount) — the run
	//     below executes the non-delete part and completes.
	//   - Switch on + gate NOT tripped: the deletes are small
	//     enough to flow; the whole plan runs, deletes included.
	//   - Switch on + gate tripped: strip the deletes, run only
	//     the non-delete part, and record the deletes as
	//     "已暂挂，等确认" — never fail the run, never delete
	//     unattended, never silently drop the deletes either.
	// Either way the run record carries DeferredDeletes so the
	// desktop history shows what was held back.
	deferred := plan.DeferredDeleteCount
	// Recompute the gate here from the live policy — the plan
	// was previewed moments ago, but the thresholds could have
	// changed since; the runner never trusts a stored flag.
	if plan.HasDeletes {
		pol := r.app.DeletePolicyFor(&job)
		if tripped, reason := pol.GateTripped(plan.CountDeletes(), plan.TargetFileCount); tripped {
			plan.GateTriggered = true
			plan.GateReason = reason
		}
	}
	if plan.HasDeletes && plan.GateTriggered {
		deferred += plan.DeleteCount
		plan = plan.WithoutDeletes()
		r.logf("agentrun: job %q: %s — 删除已暂挂，等确认，本次只同步非删除部分", job.Name, plan.GateReason)
		r.emit(job.ID, agentapi.Event{Type: "plan", RunID: runID, Message: fmt.Sprintf("删除安全闸已触发（%s），删除已暂挂，等确认", plan.GateReason)})
	}
	run.DeferredDeletes = deferred
	if deferred > 0 {
		summary.DeferredDeletes = deferred
		run.Plan = &summary
	}
	items := planItems(runID, plan)
	_ = r.db.SaveRunItems(runID, items)
	executed, err := r.app.RunJobAsAgent(ctx, &job, plan)
	if err != nil {
		fail(fmt.Errorf("执行同步失败：%w", err), true)
		return
	}
	now := time.Now().UTC()
	executed.ID = runID
	executed.Status = domain.RunCompleted
	executed.EndedAt = &now
	executed.Plan = run.Plan
	executed.DeferredDeletes = deferred
	_ = r.db.SaveRun(executed)
	for i := range items {
		items[i].Done = true
	}
	_ = r.db.SaveRunItems(runID, items)
	r.markJobRun(job, true)
	doneMsg := "已完成"
	if deferred > 0 {
		doneMsg = fmt.Sprintf("已完成（%d 个删除已暂挂，等确认）", deferred)
	}
	r.emit(job.ID, agentapi.Event{Type: "done", RunID: runID, Message: doneMsg, Total: int64(len(items))})
	r.logf("agentrun: job %q completed (%d items, %d deletes deferred)", job.Name, len(items), deferred)
}

// planItems converts a sync plan into persisted run items.
func planItems(runID string, plan *syncengine.Plan) []domain.RunItem {
	out := make([]domain.RunItem, 0, len(plan.Items))
	for _, it := range plan.Items {
		out = append(out, domain.RunItem{
			RunID: runID, Path: it.Path, Action: it.Action,
			Size: it.Size, Note: it.Note,
		})
	}
	return out
}

func (r *Runner) scheduleRetry(job domain.SyncJob) {
	r.mu.Lock()
	if t, ok := r.retries[job.ID]; ok {
		t.Stop()
	}
	j := job
	r.retries[job.ID] = time.AfterFunc(retryDelay, func() {
		r.mu.Lock()
		delete(r.retries, j.ID)
		r.mu.Unlock()
		r.logf("agentrun: job %q: retrying after failure (10 min later)", j.Name)
		r.executeJob(context.Background(), j, "", true)
	})
	r.mu.Unlock()
	r.logf("agentrun: job %q will retry once in 10 minutes", job.Name)
}

// markJobRun records the outcome on the stored job row.
func (r *Runner) markJobRun(job domain.SyncJob, ok bool) {
	now := time.Now().UTC()
	job.LastRunAt = &now
	job.LastRunOK = ok
	if next, err := scheduler.NextRun(job.Schedule, now); err == nil && !next.IsZero() {
		job.NextRunAt = &next
	}
	_ = r.db.SaveSyncJob(&job)
}

func (r *Runner) updateNextRun(jobID string, next *time.Time) {
	jobs, err := r.db.ListSyncJobs()
	if err != nil {
		return
	}
	for _, j := range jobs {
		if j.ID == jobID {
			j.NextRunAt = next
			_ = r.db.SaveSyncJob(j)
			return
		}
	}
}
