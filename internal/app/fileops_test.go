// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

// fileops_test.go — the right-click menu operations (v0.10.0).
// Local-provider tests run against the real filesystem; the
// cross-cloud move test runs against the fake MEGA driver in
// mounts_test.go (in-memory files, OpenRead faults driven by
// fakeMegaFailRead), with the worker simulated by calling
// ExecuteTransfer directly.

func fakeMegaProviderOf(t *testing.T, a *App, id string) *fakeMegaProvider {
	t.Helper()
	p, err := a.ProviderFor(id)
	if err != nil {
		t.Fatalf("ProviderFor(%s): %v", id, err)
	}
	f, ok := p.(*fakeMegaProvider)
	if !ok {
		t.Fatalf("provider for %s is %T, want *fakeMegaProvider", id, p)
	}
	return f
}

func seedFakeMegaFile(p *fakeMegaProvider, ref, content string) {
	p.files[ref] = fakeMegaEntry{name: filepath.Base(ref), content: []byte(content)}
}

func resetFakeMegaFileOps(t *testing.T) {
	t.Helper()
	fakeMegaSessionErr = nil
	fakeMegaExportSID = "test-session-id"
	fakeMegaFailRead = nil
}

func addLocalMount(t *testing.T, a *App, dir, name string) *domain.Mount {
	t.Helper()
	m, err := a.AddMountWithCredentials(context.Background(), AddMountInput{
		DisplayName: name, Provider: domain.ProviderLocal, LocalPath: dir,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(local): %v", err)
	}
	return m
}

func writeLocal(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteNodesLocal(t *testing.T) {
	dir := t.TempDir()
	writeLocal(t, dir, "a.txt", "a")
	writeLocal(t, dir, "b.txt", "b")
	writeLocal(t, dir, "sub/c.txt", "c")
	a := newTestApp(t)
	m := addLocalMount(t, a, dir, "本地")
	ctx := context.Background()
	res, err := a.DeleteNodes(ctx, m.ID, []string{"a.txt", "sub"})
	if err != nil {
		t.Fatalf("DeleteNodes: %v", err)
	}
	if len(res.Done) != 2 {
		t.Fatalf("Done = %v, want [a.txt sub]", res.Done)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("Failed = %+v, want none", res.Failed)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("a.txt still on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub")); !os.IsNotExist(err) {
		t.Fatalf("sub/ still on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatalf("b.txt must survive: %v", err)
	}
	// Empty selection and unknown mount fail outright.
	if _, err := a.DeleteNodes(ctx, m.ID, nil); err == nil {
		t.Fatal("DeleteNodes with no refs must fail")
	}
	if _, err := a.DeleteNodes(ctx, "mnt-nope", []string{"a.txt"}); err == nil || !strings.Contains(err.Error(), "unknown mount") {
		t.Fatalf("DeleteNodes on an unknown mount: got %v, want unknown mount", err)
	}
}

// TestDeleteNodesPerItemFailure: one bad ref only fails itself —
// the rest still run. The fake MEGA driver errors on a missing
// ref (its Delete Stats first); the local driver is idempotent
// instead (os.RemoveAll succeeds on a missing path), so the
// failure case lives here, not in TestDeleteNodesLocal.
func TestDeleteNodesPerItemFailure(t *testing.T) {
	resetFakeMegaFileOps(t)
	fakeMegaCaps = domain.Caps{CanRename: true, CanDelete: true, DeleteToTrash: true}
	a := newTestApp(t)
	registerFakeMega(t)
	m := addFakeMegaMount(t, a)
	p := fakeMegaProviderOf(t, a, m.ID)
	seedFakeMegaFile(p, "a.txt", "a")
	seedFakeMegaFile(p, "b.txt", "b")
	ctx := context.Background()
	res, err := a.DeleteNodes(ctx, m.ID, []string{"a.txt", "missing.txt"})
	if err != nil {
		t.Fatalf("DeleteNodes: %v", err)
	}
	if len(res.Done) != 1 || res.Done[0] != "a.txt" {
		t.Fatalf("Done = %v, want [a.txt]", res.Done)
	}
	if len(res.Failed) != 1 || res.Failed[0].Path != "missing.txt" {
		t.Fatalf("Failed = %+v, want one failure for missing.txt", res.Failed)
	}
	if _, err := p.Stat(ctx, "b.txt"); err != nil {
		t.Fatalf("b.txt must survive: %v", err)
	}
}

func TestRenameNodeLocal(t *testing.T) {
	dir := t.TempDir()
	writeLocal(t, dir, "a.txt", "a")
	a := newTestApp(t)
	m := addLocalMount(t, a, dir, "本地")
	ctx := context.Background()
	dst, err := a.RenameNode(ctx, m.ID, "a.txt", "renamed.txt")
	if err != nil {
		t.Fatalf("RenameNode: %v", err)
	}
	if dst != "renamed.txt" {
		t.Fatalf("new ref = %q, want \"renamed.txt\"", dst)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("old name still on disk after rename")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "renamed.txt")); err != nil || string(b) != "a" {
		t.Fatalf("renamed file: %v %q", err, b)
	}
	// Renaming into a subfolder keeps the directory: "sub/x.txt"
	// renamed to "y.txt" stays "sub/y.txt".
	writeLocal(t, dir, "sub/x.txt", "x")
	dst, err = a.RenameNode(ctx, m.ID, "sub/x.txt", "y.txt")
	if err != nil || dst != "sub/y.txt" {
		t.Fatalf("RenameNode(sub/x.txt): dst=%q err=%v, want sub/y.txt", dst, err)
	}
	// Bad names are rejected, never passed to the driver.
	for _, bad := range []string{"", "  ", "x/y", `x\y`, ".", ".."} {
		if _, err := a.RenameNode(ctx, m.ID, "renamed.txt", bad); err == nil {
			t.Fatalf("RenameNode(%q) must fail", bad)
		}
	}
	// Same name is a no-op, not an error.
	if dst, err := a.RenameNode(ctx, m.ID, "renamed.txt", "renamed.txt"); err != nil || dst != "renamed.txt" {
		t.Fatalf("RenameNode to the same name: dst=%q err=%v", dst, err)
	}
}

func TestMoveNodesSameMountLocal(t *testing.T) {
	dir := t.TempDir()
	writeLocal(t, dir, "a.txt", "a")
	if err := os.Mkdir(filepath.Join(dir, "目标"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t)
	m := addLocalMount(t, a, dir, "本地")
	ctx := context.Background()
	res, err := a.MoveNodes(ctx, m.ID, []string{"a.txt"}, m.ID, "目标")
	if err != nil {
		t.Fatalf("MoveNodes: %v", err)
	}
	// Same-mount move is a synchronous Rename: Done, no transfers.
	if len(res.Done) != 1 || len(res.Transfers) != 0 {
		t.Fatalf("MoveNodes same-mount: Done=%v Transfers=%d, want 1 done and no transfers", res.Done, len(res.Transfers))
	}
	if _, err := os.Stat(filepath.Join(dir, "目标", "a.txt")); err != nil {
		t.Fatalf("moved file not at 目标/a.txt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("source still at a.txt after the move")
	}
}

func TestMoveNodesSameMountGuards(t *testing.T) {
	dir := t.TempDir()
	writeLocal(t, dir, "a.txt", "a")
	writeLocal(t, dir, "照片/x.txt", "x")
	a := newTestApp(t)
	m := addLocalMount(t, a, dir, "本地")
	ctx := context.Background()
	// A file moved to the folder it already sits in is skipped.
	res, err := a.MoveNodes(ctx, m.ID, []string{"a.txt"}, m.ID, "")
	if err != nil {
		t.Fatalf("MoveNodes: %v", err)
	}
	if len(res.Done) != 0 || len(res.Failed) != 1 {
		t.Fatalf("moving a file onto itself: Done=%v Failed=%+v", res.Done, res.Failed)
	}
	// A folder moved into itself is skipped.
	res, err = a.MoveNodes(ctx, m.ID, []string{"照片"}, m.ID, "照片")
	if err != nil {
		t.Fatalf("MoveNodes: %v", err)
	}
	if len(res.Done) != 0 || len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Reason, "自己") {
		t.Fatalf("moving a folder into itself: Done=%v Failed=%+v", res.Done, res.Failed)
	}
	if _, err := os.Stat(filepath.Join(dir, "照片", "x.txt")); err != nil {
		t.Fatalf("folder must be untouched: %v", err)
	}
}

func TestMoveNodesCrossMountOnlyDeletesCopiedSources(t *testing.T) {
	resetFakeMegaFileOps(t)
	fakeMegaCaps = domain.Caps{CanRename: true, CanDelete: true, DeleteToTrash: true}
	fakeMegaFailRead = map[string]bool{"坏.txt": true}
	a := newTestApp(t)
	registerFakeMega(t)
	srcM := addFakeMegaMountAs(t, a, "源盘")
	dstM := addFakeMegaMountAs(t, a, "目标盘")
	srcP := fakeMegaProviderOf(t, a, srcM.ID)
	seedFakeMegaFile(srcP, "好.txt", "hello")
	seedFakeMegaFile(srcP, "坏.txt", "world")
	seedFakeMegaFile(srcP, "照片/a.jpg", "pic")
	ctx := context.Background()
	res, err := a.MoveNodes(ctx, srcM.ID, []string{"好.txt", "坏.txt", "照片"}, dstM.ID, "")
	if err != nil {
		t.Fatalf("MoveNodes: %v", err)
	}
	// Three files expanded (the folder contributes its one file);
	// nothing is Done synchronously on the cross-mount path.
	if len(res.Transfers) != 3 {
		t.Fatalf("Transfers = %d, want 3 (好.txt, 坏.txt, 照片/a.jpg)", len(res.Transfers))
	}
	if len(res.Done) != 0 {
		t.Fatalf("cross-mount Done = %v, want none (copies run async)", res.Done)
	}
	for _, tr := range res.Transfers {
		if !tr.DeleteSource {
			t.Fatalf("move transfer %s must carry DeleteSource", tr.ID)
		}
	}
	// Simulate the worker: execute each queued copy.
	for _, tr := range res.Transfers {
		_ = a.ExecuteTransfer(ctx, tr, func(done, speedBps int64) {})
	}
	// The good copy moved: gone from the source, present at the
	// target with its content.
	if _, err := srcP.Stat(ctx, "好.txt"); err == nil {
		t.Fatal("好.txt must be deleted from the source after a successful copy")
	}
	dstP := fakeMegaProviderOf(t, a, dstM.ID)
	if n, err := dstP.Stat(ctx, "好.txt"); err != nil || n.Size != 5 {
		t.Fatalf("好.txt at the target: %+v %v", n, err)
	}
	// The failed copy never deleted its source, and the target
	// never got it.
	if _, err := srcP.Stat(ctx, "坏.txt"); err != nil {
		t.Fatalf("坏.txt must survive a failed copy: %v", err)
	}
	if _, err := dstP.Stat(ctx, "坏.txt"); err == nil {
		t.Fatal("坏.txt must not be at the target after a failed copy")
	}
	// The folder's file moved too.
	if _, err := srcP.Stat(ctx, "照片/a.jpg"); err == nil {
		t.Fatal("照片/a.jpg must be deleted from the source after a successful copy")
	}
	if _, err := dstP.Stat(ctx, "照片/a.jpg"); err != nil {
		t.Fatalf("照片/a.jpg must be at the target: %v", err)
	}
}

func TestMoveNodesCrossMountEmptyFolder(t *testing.T) {
	// An empty folder has no files to copy: the move creates it
	// at the target and deletes the source synchronously, so it
	// is not a silent no-op.
	resetFakeMegaFileOps(t)
	fakeMegaCaps = domain.Caps{CanRename: true, CanDelete: true, DeleteToTrash: true}
	a := newTestApp(t)
	registerFakeMega(t)
	srcM := addFakeMegaMountAs(t, a, "源盘")
	dstM := addFakeMegaMountAs(t, a, "目标盘")
	srcP := fakeMegaProviderOf(t, a, srcM.ID)
	ctx := context.Background()
	if err := srcP.Mkdir(ctx, "空文件夹"); err != nil {
		t.Fatalf("seed empty folder: %v", err)
	}
	res, err := a.MoveNodes(ctx, srcM.ID, []string{"空文件夹"}, dstM.ID, "")
	if err != nil {
		t.Fatalf("MoveNodes: %v", err)
	}
	if len(res.Transfers) != 0 {
		t.Fatalf("Transfers = %d, want 0 (no files inside)", len(res.Transfers))
	}
	if len(res.Done) != 1 || res.Done[0] != "空文件夹" {
		t.Fatalf("Done = %v, want [空文件夹]", res.Done)
	}
	if _, err := srcP.Stat(ctx, "空文件夹"); err == nil {
		t.Fatal("空文件夹 must be deleted from the source")
	}
	dstP := fakeMegaProviderOf(t, a, dstM.ID)
	if n, err := dstP.Stat(ctx, "空文件夹"); err != nil || !n.IsDir {
		t.Fatalf("空文件夹 at the target: %+v %v", n, err)
	}
}

func TestFileOpsCapsDenied(t *testing.T) {
	resetFakeMegaFileOps(t)
	fakeMegaCaps = domain.Caps{} // no CanDelete / CanRename
	a := newTestApp(t)
	registerFakeMega(t)
	m := addFakeMegaMount(t, a)
	ctx := context.Background()
	if _, err := a.DeleteNodes(ctx, m.ID, []string{"a.txt"}); err == nil || !strings.Contains(err.Error(), "不支持删除") {
		t.Fatalf("DeleteNodes without CanDelete: got %v, want 不支持删除", err)
	}
	if _, err := a.RenameNode(ctx, m.ID, "a.txt", "b.txt"); err == nil || !strings.Contains(err.Error(), "不支持重命名") {
		t.Fatalf("RenameNode without CanRename: got %v, want 不支持重命名", err)
	}
	if _, err := a.MoveNodes(ctx, m.ID, []string{"a.txt"}, m.ID, ""); err == nil || !strings.Contains(err.Error(), "不支持移动") {
		t.Fatalf("MoveNodes without CanRename: got %v, want 不支持移动", err)
	}
}
