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
