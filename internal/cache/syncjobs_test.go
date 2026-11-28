// SPDX-License-Identifier: MPL-2.0
package cache

import (
	"path/filepath"
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

func TestSyncJobsRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer db.Close()

	j := &domain.SyncJob{
		ID: "job-1", Name: "照片备份", SrcMount: "m1", SrcRef: "/照片",
		DstMount: "m2", DstRef: "/备份", Direction: domain.DirectionMirror,
		Compare: domain.CompareSizeTime, Exclude: []string{"*.tmp", ".DS_Store"},
		RunOn: domain.RunOnLocal, AgentID: "", Enabled: true,
	}
	if err := db.SaveSyncJob(j); err != nil {
		t.Fatalf("SaveSyncJob: %v", err)
	}
	jobs, err := db.ListSyncJobs()
	if err != nil {
		t.Fatalf("ListSyncJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Name != "照片备份" || len(jobs[0].Exclude) != 2 {
		t.Fatalf("unexpected jobs after save: %+v", jobs)
	}
	if jobs[0].Direction != domain.DirectionMirror || jobs[0].RunOn != domain.RunOnLocal {
		t.Fatalf("fields not preserved: %+v", jobs[0])
	}
	// Update in place, then delete.
	j.Name = "照片备份 v2"
	if err := db.SaveSyncJob(j); err != nil {
		t.Fatalf("SaveSyncJob update: %v", err)
	}
	jobs, _ = db.ListSyncJobs()
	if len(jobs) != 1 || jobs[0].Name != "照片备份 v2" {
		t.Fatalf("update did not stick: %+v", jobs)
	}
	if err := db.DeleteSyncJob("job-1"); err != nil {
		t.Fatalf("DeleteSyncJob: %v", err)
	}
	jobs, _ = db.ListSyncJobs()
	if len(jobs) != 0 {
		t.Fatalf("expected no jobs after delete, got %d", len(jobs))
	}
}

// TestSyncJobsDeleteEnabledRoundTrip: the per-job delete override
// is tri-state — nil follows the global default, true/false
// override it — and survives a save/load cycle.
func TestSyncJobsDeleteEnabledRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer db.Close()

	base := &domain.SyncJob{
		ID: "job-del", Name: "删开关", SrcMount: "m1", SrcRef: "/a",
		DstMount: "m2", DstRef: "/b", Direction: domain.DirectionMirror,
		Compare: domain.CompareSizeTime, RunOn: domain.RunOnLocal, Enabled: true,
	}
	// nil: follows global.
	if err := db.SaveSyncJob(base); err != nil {
		t.Fatalf("SaveSyncJob: %v", err)
	}
	jobs, _ := db.ListSyncJobs()
	if jobs[0].DeleteEnabled != nil {
		t.Fatalf("nil override must round-trip as nil, got %+v", jobs[0].DeleteEnabled)
	}
	// true, then false.
	for _, v := range []bool{true, false} {
		j := *base
		j.DeleteEnabled = domain.BoolPtr(v)
		if err := db.SaveSyncJob(&j); err != nil {
			t.Fatalf("SaveSyncJob(%v): %v", v, err)
		}
		jobs, _ = db.ListSyncJobs()
		if jobs[0].DeleteEnabled == nil || *jobs[0].DeleteEnabled != v {
			t.Fatalf("override %v must round-trip, got %+v", v, jobs[0].DeleteEnabled)
		}
	}
	// Back to nil (clear the override).
	j := *base
	j.DeleteEnabled = nil
	if err := db.SaveSyncJob(&j); err != nil {
		t.Fatalf("SaveSyncJob(nil): %v", err)
	}
	jobs, _ = db.ListSyncJobs()
	if jobs[0].DeleteEnabled != nil {
		t.Fatalf("cleared override must round-trip as nil, got %+v", jobs[0].DeleteEnabled)
	}
}
