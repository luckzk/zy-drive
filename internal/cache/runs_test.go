// SPDX-License-Identifier: MPL-2.0
package cache

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

func TestRunsRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Truncate(time.Second)
	run := &domain.Run{
		ID: "run-1", JobID: "job-1", Status: domain.RunRunning,
		StartedAt: now, Plan: &domain.PlanSummary{ToUpload: 2, ToDelete: 1},
	}
	if err := db.SaveRun(run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	items := []domain.RunItem{
		{RunID: "run-1", Path: "a.txt", Action: domain.ActionUpload, Size: 3},
		{RunID: "run-1", Path: "old.txt", Action: domain.ActionDelete, Size: 0},
	}
	if err := db.SaveRunItems("run-1", items); err != nil {
		t.Fatalf("SaveRunItems: %v", err)
	}
	// Finish the run: same ID upserts.
	ended := now.Add(time.Minute)
	run.Status = domain.RunCompleted
	run.EndedAt = &ended
	if err := db.SaveRun(run); err != nil {
		t.Fatalf("SaveRun (finish): %v", err)
	}
	runs, err := db.ListRuns("job-1", 0)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns = %d runs, want 1", len(runs))
	}
	got := runs[0]
	if got.Status != domain.RunCompleted || got.Plan == nil || got.Plan.ToUpload != 2 || got.Plan.ToDelete != 1 {
		t.Fatalf("run = %+v, want completed with plan 2/1", got)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(ended) {
		t.Fatalf("run EndedAt = %v, want %v", got.EndedAt, ended)
	}
	gotItems, err := db.ListRunItems("run-1")
	if err != nil {
		t.Fatalf("ListRunItems: %v", err)
	}
	if len(gotItems) != 2 || gotItems[0].Path != "a.txt" || gotItems[1].Path != "old.txt" {
		t.Fatalf("items = %+v, want [a.txt old.txt]", gotItems)
	}
	// A second run lists newest first.
	run2 := &domain.Run{ID: "run-2", JobID: "job-1", Status: domain.RunFailed, StartedAt: now.Add(2 * time.Minute), Error: "boom"}
	if err := db.SaveRun(run2); err != nil {
		t.Fatalf("SaveRun(run2): %v", err)
	}
	runs, err = db.ListRuns("job-1", 1)
	if err != nil || len(runs) != 1 || runs[0].ID != "run-2" {
		t.Fatalf("ListRuns(limit 1) = %+v, %v, want [run-2]", runs, err)
	}
	// Deleting the job's runs removes rows + items.
	if err := db.DeleteRunsForJob("job-1"); err != nil {
		t.Fatalf("DeleteRunsForJob: %v", err)
	}
	runs, err = db.ListRuns("job-1", 0)
	if err != nil || len(runs) != 0 {
		t.Fatalf("runs after delete = %d, %v, want 0", len(runs), err)
	}
	if items, err := db.ListRunItems("run-1"); err != nil || len(items) != 0 {
		t.Fatalf("items after delete = %d, %v, want 0", len(items), err)
	}
}
