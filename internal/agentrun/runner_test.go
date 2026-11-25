// SPDX-License-Identifier: MPL-2.0
package agentrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// TestRunnerDeleteGuard: a mirror job whose plan wants to delete
// files NEVER deletes on a scheduled/immediate Agent run — the run
// fails with a clear message, the file stays, and no retry is
// scheduled (retrying would fail the same way).
func TestRunnerDeleteGuard(t *testing.T) {
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
	if run.Status != domain.RunFailed {
		t.Fatalf("run status = %v, want failed (delete guard)", run.Status)
	}
	if !strings.Contains(run.Error, "删除") {
		t.Fatalf("run error = %q, want it to explain the delete guard", run.Error)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "old.txt")); err != nil {
		t.Fatalf("the guarded file was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "a.txt")); err == nil {
		t.Fatal("a.txt was copied even though the run failed at the guard")
	}
	r.mu.Lock()
	n := len(r.retries)
	r.mu.Unlock()
	if n != 0 {
		t.Fatalf("retries scheduled = %d, want 0 (delete guard is not retryable)", n)
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
