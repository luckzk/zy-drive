// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

// addLocalMounts creates n local mounts in group, named
// "<group>-1" .. "<group>-n", and returns them in creation order.
func addLocalMounts(t *testing.T, a *App, group string, n int) []*domain.Mount {
	t.Helper()
	ctx := context.Background()
	var out []*domain.Mount
	for i := 1; i <= n; i++ {
		dir := filepath.Join(t.TempDir(), group, strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		m, err := a.AddMountWithCredentials(ctx, AddMountInput{
			DisplayName: group + "-" + strconv.Itoa(i),
			Provider:    domain.ProviderLocal, Group: group, LocalPath: dir,
		})
		if err != nil {
			t.Fatalf("AddMountWithCredentials: %v", err)
		}
		out = append(out, m)
	}
	return out
}

// groupNames lists one group's mounts by display name, in
// ListMounts (sidebar) order.
func groupNames(a *App, group string) []string {
	var out []string
	for _, m := range a.ListMounts() {
		if m.Group == group {
			out = append(out, m.DisplayName)
		}
	}
	return out
}

func equalNames(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", what, got, want)
		}
	}
}

// TestMoveMountSameGroup: dragging the 8th mount to the 4th
// position pushes the old 4th..7th down to 5th..8th.
func TestMoveMountSameGroup(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	ms := addLocalMounts(t, a, "工作", 8)
	if _, err := a.MoveMount(ctx, ms[7].ID, "工作", 3); err != nil {
		t.Fatalf("MoveMount: %v", err)
	}
	equalNames(t, "工作 after 8→4", groupNames(a, "工作"),
		[]string{"工作-1", "工作-2", "工作-3", "工作-8", "工作-4", "工作-5", "工作-6", "工作-7"})
	// Sort orders are dense 0..7 in the new order.
	for i, m := range a.ListMounts() {
		if m.SortOrder != i {
			t.Fatalf("mount %s sort_order = %d, want %d", m.DisplayName, m.SortOrder, i)
		}
	}
}

// TestMoveMountCrossGroup: both groups renumber; the source group
// closes ranks, the target group makes room.
func TestMoveMountCrossGroup(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	work := addLocalMounts(t, a, "工作", 3)
	addLocalMounts(t, a, "个人", 2)
	if _, err := a.MoveMount(ctx, work[0].ID, "个人", 1); err != nil {
		t.Fatalf("MoveMount: %v", err)
	}
	equalNames(t, "工作 after move", groupNames(a, "工作"), []string{"工作-2", "工作-3"})
	equalNames(t, "个人 after move", groupNames(a, "个人"), []string{"个人-1", "工作-1", "个人-2"})
}

// TestMoveMountEmptyGroup: moving to a group with no mounts lands
// at index 0.
func TestMoveMountEmptyGroup(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	ms := addLocalMounts(t, a, "工作", 2)
	m, err := a.MoveMount(ctx, ms[0].ID, "新分组", 0)
	if err != nil {
		t.Fatalf("MoveMount: %v", err)
	}
	if m.Group != "新分组" || m.SortOrder != 0 {
		t.Fatalf("moved mount = %+v, want group 新分组 sort_order 0", m)
	}
	equalNames(t, "工作 after move", groupNames(a, "工作"), []string{"工作-2"})
}

// TestMoveMountClampsIndex: out-of-range indices land at the ends.
func TestMoveMountClampsIndex(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	ms := addLocalMounts(t, a, "工作", 3)
	if _, err := a.MoveMount(ctx, ms[0].ID, "工作", 999); err != nil {
		t.Fatalf("MoveMount: %v", err)
	}
	equalNames(t, "clamp high", groupNames(a, "工作"), []string{"工作-2", "工作-3", "工作-1"})
	if _, err := a.MoveMount(ctx, ms[0].ID, "工作", -5); err != nil {
		t.Fatalf("MoveMount: %v", err)
	}
	equalNames(t, "clamp low", groupNames(a, "工作"), []string{"工作-1", "工作-2", "工作-3"})
}

// TestMoveMountNoOp: moving to the same position writes nothing —
// UpdatedAt is untouched and the order is unchanged.
func TestMoveMountNoOp(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	ms := addLocalMounts(t, a, "工作", 3)
	before := ms[1].UpdatedAt
	if _, err := a.MoveMount(ctx, ms[1].ID, "工作", 1); err != nil {
		t.Fatalf("MoveMount: %v", err)
	}
	if !ms[1].UpdatedAt.Equal(before) {
		t.Fatalf("no-op move changed UpdatedAt: %v -> %v", before, ms[1].UpdatedAt)
	}
	equalNames(t, "order after no-op", groupNames(a, "工作"), []string{"工作-1", "工作-2", "工作-3"})
	// Unknown mount still errors.
	if _, err := a.MoveMount(ctx, "mnt-nope", "工作", 0); err == nil {
		t.Fatal("MoveMount on an unknown mount must fail")
	}
}

// TestMoveMountPersistsAcrossRestart: the order survives a restart.
func TestMoveMountPersistsAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	ctx := context.Background()
	ms := addLocalMounts(t, a, "工作", 4)
	if _, err := a.MoveMount(ctx, ms[3].ID, "工作", 0); err != nil {
		t.Fatalf("MoveMount: %v", err)
	}
	a2 := reopenApp(t, dbPath)
	equalNames(t, "工作 after restart", groupNames(a2, "工作"),
		[]string{"工作-4", "工作-1", "工作-2", "工作-3"})
}

// TestRemoveMountClosesRanks: deleting the middle mount keeps the
// rest dense 0..n-1, in memory and after a restart.
func TestRemoveMountClosesRanks(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	ms := addLocalMounts(t, a, "工作", 3)
	if err := a.RemoveMount(ms[1].ID); err != nil {
		t.Fatalf("RemoveMount: %v", err)
	}
	equalNames(t, "工作 after remove", groupNames(a, "工作"), []string{"工作-1", "工作-3"})
	for i, m := range a.ListMounts() {
		if m.SortOrder != i {
			t.Fatalf("mount %s sort_order = %d, want %d", m.DisplayName, m.SortOrder, i)
		}
	}
	a2 := reopenApp(t, dbPath)
	equalNames(t, "工作 after restart", groupNames(a2, "工作"), []string{"工作-1", "工作-3"})
}

// TestAddMountAppendsAtEnd: new mounts land at the end of their
// group; a fresh group starts at 0.
func TestAddMountAppendsAtEnd(t *testing.T) {
	a := newTestApp(t)
	ms := addLocalMounts(t, a, "工作", 3)
	for i, m := range ms {
		if m.SortOrder != i {
			t.Fatalf("mount %s sort_order = %d, want %d", m.DisplayName, m.SortOrder, i)
		}
	}
	other := addLocalMounts(t, a, "个人", 1)
	if other[0].SortOrder != 0 {
		t.Fatalf("first mount in a new group sort_order = %d, want 0", other[0].SortOrder)
	}
}
