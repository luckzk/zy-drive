// SPDX-License-Identifier: MPL-2.0
package agentrun

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/app"
	"github.com/luckzk/zy-drive/internal/auth"
	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/platform/scheduler"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/providers/local"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// newRunnerApp builds an App with two local mounts (src/dst dirs)
// for runner tests. The runner executes through the real App +
// sync engine; only the cloud is replaced by local folders.
func newRunnerApp(t *testing.T) (*app.App, *cache.DB, string, string) {
	t.Helper()
	db, err := cache.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := storage.Default()
	func() {
		defer func() { _ = recover() }()
		reg.Register(domain.ProviderLocal, local.New)
	}()
	a := app.New(db, reg, transfer.NewQueue(db))
	secrets, err := auth.NewSecretsStore("runner-test-password", []byte("runner-salt-16by"))
	if err != nil {
		t.Fatal(err)
	}
	a.SetSecrets(secrets)
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	ctx := context.Background()
	src, err := a.AddMountWithCredentials(ctx, app.AddMountInput{
		DisplayName: "源", Provider: domain.ProviderLocal, LocalPath: srcDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	dst, err := a.AddMountWithCredentials(ctx, app.AddMountInput{
		DisplayName: "目标", Provider: domain.ProviderLocal, LocalPath: dstDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = src
	_ = dst
	return a, db, srcDir, dstDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func saveAgentJob(t *testing.T, a *app.App, srcID, dstID string, dir domain.SyncDirection, schedule string) *domain.SyncJob {
	t.Helper()
	j, err := a.SaveSyncJob(&domain.SyncJob{
		Name: "runner 测试", SrcMount: srcID, SrcRef: "", DstMount: dstID, DstRef: "",
		Direction: dir, RunOn: domain.RunOnAgent, AgentID: "ag-1",
		Schedule: schedule, Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveSyncJob: %v", err)
	}
	return j
}

func mountIDs(t *testing.T, a *app.App) (string, string) {
	t.Helper()
	mounts := a.ListMounts()
	if len(mounts) != 2 {
		t.Fatalf("mounts = %d, want 2", len(mounts))
	}
	return mounts[0].ID, mounts[1].ID
}

// waitRun polls the DB until a run for the job reaches a terminal
// state (or fails the test on timeout).
func waitRun(t *testing.T, db *cache.DB, jobID string, timeout time.Duration) *domain.Run {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		runs, err := db.ListRuns(jobID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) > 0 && runs[0].Status != domain.RunRunning {
			return runs[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no terminal run for job %s in %s", jobID, timeout)
	return nil
}

// TestRunnerImmediateRun: RunNow executes a copy job end to end —
// files land in the destination and the run record (with items) is
// persisted.
func TestRunnerImmediateRun(t *testing.T) {
	a, db, srcDir, dstDir := newRunnerApp(t)
	writeFile(t, filepath.Join(srcDir, "a.txt"), "aaa")
	writeFile(t, filepath.Join(srcDir, "sub", "b.txt"), "bbb")
	srcID, dstID := mountIDs(t, a)
	job := saveAgentJob(t, a, srcID, dstID, domain.DirectionCopy, "")
	r := New(a, db, scheduler.New())
	runID, err := r.RunNow(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if runID == "" {
		t.Fatal("RunNow returned an empty run id")
	}
	run := waitRun(t, db, job.ID, 10*time.Second)
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v (%q), want completed", run.Status, run.Error)
	}
	if run.Plan == nil || run.Plan.ToUpload != 2 {
		t.Fatalf("run plan = %+v, want 2 uploads", run.Plan)
	}
	items, err := db.ListRunItems(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("run items = %d, want 2", len(items))
	}
	for _, it := range items {
		if !it.Done {
			t.Fatalf("item %+v is not marked done", it)
		}
	}
	for rel, want := range map[string]string{"a.txt": "aaa", "sub/b.txt": "bbb"} {
		got, err := os.ReadFile(filepath.Join(dstDir, rel))
		if err != nil {
			t.Fatalf("read dst %s: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("dst %s = %q, want %q", rel, got, want)
		}
	}
	// A second immediate run while one is executing is refused.
	// (Start a slow run by blocking the source with many files is
	// overkill; the guard is exercised through the running map
	// directly: mark it and call RunNow.)
	r.mu.Lock()
	r.running[job.ID] = true
	r.mu.Unlock()
	if _, err := r.RunNow(context.Background(), job.ID); err == nil {
		t.Fatal("RunNow during an active run must be refused")
	}
	r.mu.Lock()
	delete(r.running, job.ID)
	r.mu.Unlock()
}

// TestRunnerDeleteSuspendedByDefault: a mirror job whose plan
// wants to delete files, with the delete switch off (the §20
// default), NEVER deletes on an Agent run — the run completes
// with the non-delete part, the file stays, and the delete is
// recorded as deferred ("删除已暂挂，等确认"), with no retry.
// (v0.20.0 changed this from the old behaviour, which failed the
// whole run — roadmap §22:定时任务改跑非删除部分并记“删除已暂挂，等确认”.)
func TestRunnerDeleteSuspendedByDefault(t *testing.T) {
	a, db, srcDir, dstDir := newRunnerApp(t)
	writeFile(t, filepath.Join(srcDir, "a.txt"), "aaa")
	writeFile(t, filepath.Join(dstDir, "old.txt"), "keep-me")
	srcID, dstID := mountIDs(t, a)
	job := saveAgentJob(t, a, srcID, dstID, domain.DirectionMirror, "")
	r := New(a, db, scheduler.New())
	if _, err := r.RunNow(context.Background(), job.ID); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	run := waitRun(t, db, job.ID, 10*time.Second)
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v, want completed (non-delete part runs)", run.Status)
	}
	if run.DeferredDeletes != 1 {
		t.Fatalf("run DeferredDeletes = %d, want 1", run.DeferredDeletes)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "old.txt")); err != nil {
		t.Fatalf("the suspended file was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "a.txt")); err != nil {
		t.Fatalf("the non-delete part must still run (a.txt missing): %v", err)
	}
	r.mu.Lock()
	n := len(r.retries)
	r.mu.Unlock()
	if n != 0 {
		t.Fatalf("retries scheduled = %d, want 0 (suspension is not a failure)", n)
	}
}

// TestRunnerDeleteGateSuspended: with the job-level delete switch
// ON and 21 deletes (over the §22 gate), the Agent run completes
// the non-delete part, keeps all 21 files, and records 21
// deferred deletes — it never deletes unattended past the gate.
func TestRunnerDeleteGateSuspended(t *testing.T) {
	a, db, srcDir, dstDir := newRunnerApp(t)
	writeFile(t, filepath.Join(srcDir, "a.txt"), "aaa")
	for i := 0; i < 21; i++ {
		writeFile(t, filepath.Join(dstDir, "old"+string(rune('a'+i))+".txt"), "keep-me")
	}
	srcID, dstID := mountIDs(t, a)
	job := saveAgentJob(t, a, srcID, dstID, domain.DirectionMirror, "")
	job.DeleteEnabled = domain.BoolPtr(true)
	if _, err := a.SaveSyncJob(job); err != nil {
		t.Fatalf("SaveSyncJob(delete on): %v", err)
	}
	r := New(a, db, scheduler.New())
	if _, err := r.RunNow(context.Background(), job.ID); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	run := waitRun(t, db, job.ID, 10*time.Second)
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v, want completed (gate suspends, not fails)", run.Status)
	}
	if run.DeferredDeletes != 21 {
		t.Fatalf("run DeferredDeletes = %d, want 21", run.DeferredDeletes)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "a.txt")); err != nil {
		t.Fatalf("non-delete part must run: %v", err)
	}
	for i := 0; i < 21; i++ {
		if _, err := os.Stat(filepath.Join(dstDir, "old"+string(rune('a'+i))+".txt")); err != nil {
			t.Fatalf("gated file %d was deleted: %v", i, err)
		}
	}
}

// TestRunnerDeleteAllowedSmall: with the job-level delete switch
// ON and a single delete (under the gate), the Agent run deletes
// it — small, explicitly allowed deletes still flow unattended.
func TestRunnerDeleteAllowedSmall(t *testing.T) {
	a, db, srcDir, dstDir := newRunnerApp(t)
	writeFile(t, filepath.Join(srcDir, "a.txt"), "aaa")
	writeFile(t, filepath.Join(dstDir, "old.txt"), "delete-me")
	// 20 files on both sides, so the target holds 21 files and
	// the single delete is ~5% — under both gate thresholds.
	// (With only old.txt on the target, 1 delete would be 100%
	// of the target and the gate would rightly trip.)
	for i := 0; i < 20; i++ {
		name := "common" + string(rune('a'+i)) + ".txt"
		writeFile(t, filepath.Join(srcDir, name), "c")
		writeFile(t, filepath.Join(dstDir, name), "c")
	}
	srcID, dstID := mountIDs(t, a)
	job := saveAgentJob(t, a, srcID, dstID, domain.DirectionMirror, "")
	job.DeleteEnabled = domain.BoolPtr(true)
	if _, err := a.SaveSyncJob(job); err != nil {
		t.Fatalf("SaveSyncJob(delete on): %v", err)
	}
	r := New(a, db, scheduler.New())
	if _, err := r.RunNow(context.Background(), job.ID); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	run := waitRun(t, db, job.ID, 10*time.Second)
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v, want completed", run.Status)
	}
	if run.DeferredDeletes != 0 {
		t.Fatalf("run DeferredDeletes = %d, want 0", run.DeferredDeletes)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "old.txt")); err == nil {
		t.Fatalf("allowed small delete must execute")
	}
	if _, err := os.Stat(filepath.Join(dstDir, "a.txt")); err != nil {
		t.Fatalf("a.txt must land: %v", err)
	}
}

// TestRunnerRetryOnce: a failing job schedules exactly one retry;
// a retry that fails again schedules nothing more.
func TestRunnerRetryOnce(t *testing.T) {
	a, db, _, _ := newRunnerApp(t)
	srcID, dstID := mountIDs(t, a)
	job := saveAgentJob(t, a, srcID, dstID, domain.DirectionCopy, "")
	// Break the job: point it at a mount that does not exist, so
	// the preview fails (transient-looking error).
	broken := *job
	broken.SrcMount = "mnt-does-not-exist"
	if _, err := a.SaveSyncJob(&broken); err != nil {
		t.Fatalf("SaveSyncJob(broken): %v", err)
	}
	r := New(a, db, scheduler.New())
	if _, err := r.RunNow(context.Background(), job.ID); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	run := waitRun(t, db, job.ID, 10*time.Second)
	if run.Status != domain.RunFailed {
		t.Fatalf("run status = %v, want failed", run.Status)
	}
	r.mu.Lock()
	n := len(r.retries)
	r.mu.Unlock()
	if n != 1 {
		t.Fatalf("retries scheduled = %d, want exactly 1", n)
	}
	// The retry itself failing must not schedule another one.
	r.executeJob(context.Background(), broken, "", true)
	time.Sleep(200 * time.Millisecond)
	r.mu.Lock()
	n = len(r.retries)
	r.mu.Unlock()
	if n != 1 {
		t.Fatalf("retries after a failed retry = %d, want still 1 (the first timer)", n)
	}
	// Clean up the pending timer so the test binary can exit.
	r.mu.Lock()
	for _, timer := range r.retries {
		timer.Stop()
	}
	r.mu.Unlock()
}

// TestRunnerScheduledRunFires is the item-1 acceptance test: a job
// scheduled "daily HH:MM" one minute ahead fires on the scheduler
// and leaves a completed run record — with the desktop (App)
// untouched. It waits up to ~100s for the next minute boundary.
func TestRunnerScheduledRunFires(t *testing.T) {
	a, db, srcDir, dstDir := newRunnerApp(t)
	writeFile(t, filepath.Join(srcDir, "a.txt"), "scheduled")
	srcID, dstID := mountIDs(t, a)
	nextMinute := time.Now().Add(time.Minute).Format("15:04")
	job := saveAgentJob(t, a, srcID, dstID, domain.DirectionCopy, "daily "+nextMinute)
	sched := scheduler.NewWithInterval(time.Second)
	r := New(a, db, sched)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Start(ctx)
	if err := r.RegisterJobs(); err != nil {
		t.Fatalf("RegisterJobs: %v", err)
	}
	run := waitRun(t, db, job.ID, 100*time.Second)
	if run.Status != domain.RunCompleted {
		t.Fatalf("scheduled run status = %v (%q), want completed", run.Status, run.Error)
	}
	got, err := os.ReadFile(filepath.Join(dstDir, "a.txt"))
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "scheduled" {
		t.Fatalf("dst = %q, want scheduled", got)
	}
}

// TestRunnerSubfolderRoots: a job syncing a source subfolder into
// the destination root must land the files at the destination root
// ("/sub/a.txt" -> "/a.txt"), not recreate the source folder, and
// a mirror job must not treat the destination's existing files as
// deletions just because the roots differ.
func TestRunnerSubfolderRoots(t *testing.T) {
	a, db, srcDir, dstDir := newRunnerApp(t)
	writeFile(t, filepath.Join(srcDir, "sub", "a.txt"), "aaa")
	writeFile(t, filepath.Join(srcDir, "sub", "b.txt"), "bbb")
	srcID, dstID := mountIDs(t, a)
	job, err := a.SaveSyncJob(&domain.SyncJob{
		Name: "子文件夹同步", SrcMount: srcID, SrcRef: "/sub", DstMount: dstID, DstRef: "",
		Direction: domain.DirectionCopy, RunOn: domain.RunOnAgent, AgentID: "ag-1",
		Schedule: "", Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveSyncJob: %v", err)
	}
	plan, err := a.PreviewJob(context.Background(), job)
	if err != nil {
		t.Fatalf("PreviewJob: %v", err)
	}
	if len(plan.Items) != 2 {
		t.Fatalf("plan items = %+v, want the 2 files under /sub", plan.Items)
	}
	r := New(a, db, scheduler.New())
	if _, err := r.RunNow(context.Background(), job.ID); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	run := waitRun(t, db, job.ID, 10*time.Second)
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v (%q), want completed", run.Status, run.Error)
	}
	for _, rel := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(dstDir, rel)); err != nil {
			t.Fatalf("dst %s missing: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dstDir, "sub")); err == nil {
		t.Fatal("the source subfolder was recreated at the destination")
	}
}
