// SPDX-License-Identifier: MPL-2.0
package sync

import (
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

func node(path string, isDir bool, size int64) domain.Node {
	return domain.Node{Path: path, Ref: path, Name: path, IsDir: isDir, Size: size, ModTime: time.Unix(1000, 0)}
}

// TestCompareSkipsSourceOnlyDirs: a directory that exists only on
// the source is not a change — its contents are, and the directory
// itself is created implicitly by the upload. Uploading a directory
// as a file used to crash the run (local driver: "is a directory").
func TestCompareSkipsSourceOnlyDirs(t *testing.T) {
	src := &Snapshot{Nodes: map[string]domain.Node{
		"/sub":       node("/sub", true, -1),
		"/sub/b.txt": node("/sub/b.txt", false, 3),
		"/a.txt":     node("/a.txt", false, 3),
	}}
	dst := &Snapshot{Nodes: map[string]domain.Node{}}
	changes := Compare(src, dst, domain.CompareSizeTime, nil)
	for _, c := range changes {
		if c.Path == "/sub" {
			t.Fatalf("changes include the directory /sub: %+v", changes)
		}
		if c.Src != nil && c.Src.IsDir {
			t.Fatalf("a directory change slipped through: %+v", c)
		}
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %d, want 2 (the two files)", len(changes))
	}
	// And the plan built from it has no directory upload item.
	plan := BuildPlan("job-1", domain.DirectionCopy, changes)
	if len(plan.Items) != 2 {
		t.Fatalf("plan items = %d, want 2", len(plan.Items))
	}
	for _, it := range plan.Items {
		if it.Path == "/sub" {
			t.Fatal("plan tries to upload the directory /sub")
		}
	}
	// A directory present on both sides is still "same", not a change.
	dst2 := &Snapshot{Nodes: map[string]domain.Node{
		"/sub":       node("/sub", true, -1),
		"/sub/b.txt": node("/sub/b.txt", false, 3),
		"/a.txt":     node("/a.txt", false, 3),
	}}
	changes = Compare(src, dst2, domain.CompareSizeTime, nil)
	for _, c := range changes {
		if c.Kind != ChangeSame {
			t.Fatalf("identical snapshots produced a non-same change: %+v", c)
		}
	}
}
