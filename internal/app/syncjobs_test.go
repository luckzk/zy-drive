// SPDX-License-Identifier: MPL-2.0
package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

// TestPreviewAndRunFileRootLocal is the end-to-end proof for the
// file-root fix, through the real local provider (no fake): a job
// whose source root is a single .iso file previews to exactly one
// upload, runs to completion, and lands the file's bytes on the
// target — the run the user reported as "完成 0/0/0".
func TestPreviewAndRunFileRootLocal(t *testing.T) {
	a := newTestApp(t)
	ctx := t.Context()

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	iso := []byte("fake-iso-bytes-67MB-would-go-here")
	if err := os.WriteFile(filepath.Join(srcDir, "alpine.iso"), iso, 0o644); err != nil {
		t.Fatalf("write src file: %v", err)
	}
	job := &domain.SyncJob{
		ID: "j-file-root", Name: "iso copy",
		SrcMount: domain.LocalMountID, SrcRef: "local://" + filepath.Join(srcDir, "alpine.iso"),
		DstMount: domain.LocalMountID, DstRef: "local://" + dstDir,
		Direction: domain.DirectionCopy, Compare: domain.CompareSizeTime,
		RunOn: domain.RunOnLocal,
	}
	plan, err := a.PreviewJob(ctx, job)
	if err != nil {
		t.Fatalf("PreviewJob file root: %v", err)
	}
	if len(plan.Items) != 1 || plan.Items[0].Path != "/alpine.iso" || plan.Items[0].Action != domain.ActionUpload {
		t.Fatalf("plan = %+v, want one upload of /alpine.iso", plan.Items)
	}
	run, err := a.RunJob(ctx, job, plan)
	if err != nil {
		t.Fatalf("RunJob file root: %v", err)
	}
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v, want completed", run.Status)
	}
	got, err := os.ReadFile(filepath.Join(dstDir, "alpine.iso"))
	if err != nil {
		t.Fatalf("dst file missing after run: %v", err)
	}
	if string(got) != string(iso) {
		t.Fatalf("dst file = %q, want %q", got, iso)
	}
	runs, err := a.DB.ListRuns(job.ID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns: %v, %d runs, want 1 recorded run", err, len(runs))
	}
	if runs[0].Status != domain.RunCompleted {
		t.Fatalf("recorded run status = %v, want completed", runs[0].Status)
	}
}

// TestPreviewAndRunFileToFileLocal: both roots are single files
// with different names. The target file receives the source
// file's content (one update, not an upload-plus-delete pair),
// and the source file is untouched.
func TestPreviewAndRunFileToFileLocal(t *testing.T) {
	a := newTestApp(t)
	ctx := t.Context()

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "a.iso"), []byte("new-content"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "b.iso"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write dst: %v", err)
	}
	job := &domain.SyncJob{
		ID: "j-file-file", Name: "iso replace",
		SrcMount: domain.LocalMountID, SrcRef: "local://" + filepath.Join(srcDir, "a.iso"),
		DstMount: domain.LocalMountID, DstRef: "local://" + filepath.Join(dstDir, "b.iso"),
		Direction: domain.DirectionCopy, Compare: domain.CompareSizeTime,
		RunOn: domain.RunOnLocal,
	}
	plan, err := a.PreviewJob(ctx, job)
	if err != nil {
		t.Fatalf("PreviewJob file-to-file: %v", err)
	}
	if len(plan.Items) != 1 || plan.Items[0].Action != domain.ActionUpdate {
		t.Fatalf("plan = %+v, want one update (no spurious delete)", plan.Items)
	}
	run, err := a.RunJob(ctx, job, plan)
	if err != nil {
		t.Fatalf("RunJob file-to-file: %v", err)
	}
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v, want completed", run.Status)
	}
	got, err := os.ReadFile(filepath.Join(dstDir, "b.iso"))
	if err != nil {
		t.Fatalf("dst file missing after run: %v", err)
	}
	if string(got) != "new-content" {
		t.Fatalf("dst file = %q, want %q", got, "new-content")
	}
	if _, err := os.Stat(filepath.Join(srcDir, "a.iso")); err != nil {
		t.Fatalf("source file must survive: %v", err)
	}
}

// TestPreviewFolderSourceFileTarget: a whole folder cannot sync
// into one file — PreviewJob must say so clearly instead of
// planning against a one-entry target snapshot.
func TestPreviewFolderSourceFileTarget(t *testing.T) {
	a := newTestApp(t)
	ctx := t.Context()

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "single.txt"), []byte("y"), 0o644); err != nil {
		t.Fatalf("write dst: %v", err)
	}
	job := &domain.SyncJob{
		ID: "j-folder-file", Name: "folder into file",
		SrcMount: domain.LocalMountID, SrcRef: "local://" + srcDir,
		DstMount: domain.LocalMountID, DstRef: "local://" + filepath.Join(dstDir, "single.txt"),
		Direction: domain.DirectionCopy, Compare: domain.CompareSizeTime,
		RunOn: domain.RunOnLocal,
	}
	_, err := a.PreviewJob(ctx, job)
	if err == nil {
		t.Fatal("PreviewJob folder-into-file must error, got a plan")
	}
	if !strings.Contains(err.Error(), "single file") {
		t.Fatalf("error %q must say the target is a single file", err)
	}
}

// TestPreviewAndRunMirrorDeleteDefaultOff (§20, local end to end):
// with the global delete switch off (the default), a mirror
// preview strips the delete, counts it deferred, and the run
// completes with the target-only file still on disk.
func TestPreviewAndRunMirrorDeleteDefaultOff(t *testing.T) {
	a := newTestApp(t)
	ctx := t.Context()

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write dst: %v", err)
	}
	job := &domain.SyncJob{
		ID: "j-mirror-off", Name: "mirror off",
		SrcMount: domain.LocalMountID, SrcRef: "local://" + srcDir,
		DstMount: domain.LocalMountID, DstRef: "local://" + dstDir,
		Direction: domain.DirectionMirror, Compare: domain.CompareSizeTime,
		RunOn: domain.RunOnLocal,
	}
	plan, err := a.PreviewJob(ctx, job)
	if err != nil {
		t.Fatalf("PreviewJob: %v", err)
	}
	if plan.HasDeletes || plan.DeleteCount != 0 {
		t.Fatalf("switch off: plan must not carry deletes, got %+v", plan)
	}
	if !plan.DeletesDeferred || plan.DeferredDeleteCount != 1 {
		t.Fatalf("switch off: plan must defer 1 delete, got deferred=%v count=%d", plan.DeletesDeferred, plan.DeferredDeleteCount)
	}
	run, err := a.RunJob(ctx, job, plan)
	if err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v, want completed", run.Status)
	}
	if run.DeferredDeletes != 1 {
		t.Fatalf("run DeferredDeletes = %d, want 1", run.DeferredDeletes)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "stale.txt")); err != nil {
		t.Fatalf("switch off: stale.txt must survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "keep.txt")); err != nil {
		t.Fatalf("keep.txt must land: %v", err)
	}
}

// TestPreviewAndRunMirrorDeleteJobOn (§20 per-job override): the
// same mirror job with DeleteEnabled=true flows the delete even
// though the global switch is off.
func TestPreviewAndRunMirrorDeleteJobOn(t *testing.T) {
	a := newTestApp(t)
	ctx := t.Context()

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write dst: %v", err)
	}
	// 20 files on both sides, so the target holds 21 files and
	// the single delete is ~5% — under both gate thresholds.
	// (With only stale.txt on the target, 1 delete would be
	// 100% of the target and the gate would rightly trip.)
	for i := 0; i < 20; i++ {
		name := "common" + string(rune('a'+i)) + ".txt"
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte("c"), 0o644); err != nil {
			t.Fatalf("write src common: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dstDir, name), []byte("c"), 0o644); err != nil {
			t.Fatalf("write dst common: %v", err)
		}
	}
	job := &domain.SyncJob{
		ID: "j-mirror-on", Name: "mirror on",
		SrcMount: domain.LocalMountID, SrcRef: "local://" + srcDir,
		DstMount: domain.LocalMountID, DstRef: "local://" + dstDir,
		Direction: domain.DirectionMirror, Compare: domain.CompareSizeTime,
		RunOn: domain.RunOnLocal, DeleteEnabled: domain.BoolPtr(true),
	}
	plan, err := a.PreviewJob(ctx, job)
	if err != nil {
		t.Fatalf("PreviewJob: %v", err)
	}
	if !plan.HasDeletes || plan.DeleteCount != 1 {
		t.Fatalf("job override on: plan must carry 1 delete, got %+v", plan)
	}
	if plan.GateTriggered {
		t.Fatalf("1 delete must not trip the gate")
	}
	run, err := a.RunJob(ctx, job, plan)
	if err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v, want completed", run.Status)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "stale.txt")); err == nil {
		t.Fatalf("job override on: stale.txt must be deleted")
	}
}

// TestPreviewMirrorDeleteGateTrips (§22, local end to end): 21
// target-only files with the job switch on trip the gate in the
// preview; running that plan unconfirmed is refused by the engine.
func TestPreviewMirrorDeleteGateTrips(t *testing.T) {
	a := newTestApp(t)
	ctx := t.Context()

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	for i := 0; i < 21; i++ {
		name := "stale" + string(rune('a'+i)) + ".txt"
		if err := os.WriteFile(filepath.Join(dstDir, name), []byte("stale"), 0o644); err != nil {
			t.Fatalf("write dst: %v", err)
		}
	}
	job := &domain.SyncJob{
		ID: "j-mirror-gate", Name: "mirror gate",
		SrcMount: domain.LocalMountID, SrcRef: "local://" + srcDir,
		DstMount: domain.LocalMountID, DstRef: "local://" + dstDir,
		Direction: domain.DirectionMirror, Compare: domain.CompareSizeTime,
		RunOn: domain.RunOnLocal, DeleteEnabled: domain.BoolPtr(true),
	}
	plan, err := a.PreviewJob(ctx, job)
	if err != nil {
		t.Fatalf("PreviewJob: %v", err)
	}
	if !plan.GateTriggered || plan.GateReason == "" {
		t.Fatalf("21 deletes must trip the gate, got %+v", plan)
	}
	if _, err := a.RunJob(ctx, job, plan); err == nil {
		t.Fatalf("unconfirmed gate plan must be refused")
	}
	// Confirmed: the run goes through and deletes.
	plan.ConfirmDeletes = true
	run, err := a.RunJob(ctx, job, plan)
	if err != nil {
		t.Fatalf("confirmed RunJob: %v", err)
	}
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v, want completed", run.Status)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "stalea.txt")); err == nil {
		t.Fatalf("confirmed run must delete the stale files")
	}
}
