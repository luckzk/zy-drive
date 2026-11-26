// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// versions_test.go — the app-layer history + server-side copy paths
// (v0.12.0). The fake MEGA driver stands in for a Versioner +
// Copier cloud (its Upload records pre-overwrite bytes as
// versions, its Copy never touches OpenRead); the local driver
// stands in for a cloud with neither, so the streaming fallback
// is exercised for real.

func TestListVersionsRestoreVersionFakeMega(t *testing.T) {
	resetFakeMegaFileOps(t)
	fakeMegaCaps = domain.Caps{CanRename: true, CanDelete: true, DeleteToTrash: true,
		SupportsVersions: true, SupportsServerSideCopy: true}
	a := newTestApp(t)
	registerFakeMega(t)
	m := addFakeMegaMount(t, a)
	p := fakeMegaProviderOf(t, a, m.ID)
	ctx := context.Background()
	if _, err := p.Upload(ctx, "a.txt", strings.NewReader("v1"), 2); err != nil {
		t.Fatalf("Upload v1: %v", err)
	}
	if _, err := p.Upload(ctx, "a.txt", strings.NewReader("v2"), 2); err != nil {
		t.Fatalf("Upload v2: %v", err)
	}
	vs, err := a.ListVersions(ctx, m.ID, "a.txt")
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(vs) != 1 {
		t.Fatalf("ListVersions: got %d, want 1", len(vs))
	}
	if vs[0].Extra["versionId"] == "" {
		t.Fatalf("version node must carry Extra[versionId], got %+v", vs[0])
	}
	if err := a.RestoreVersion(ctx, m.ID, "a.txt", vs[0].Extra["versionId"]); err != nil {
		t.Fatalf("RestoreVersion: %v", err)
	}
	rc, err := p.OpenRead(ctx, "a.txt")
	if err != nil {
		t.Fatalf("OpenRead after restore: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "v1" {
		t.Fatalf("content after restore: %q, want %q", got, "v1")
	}
	// A restore is itself undoable: the v2 head became a version.
	vs, err = a.ListVersions(ctx, m.ID, "a.txt")
	if err != nil || len(vs) != 2 {
		t.Fatalf("ListVersions after restore: %v, %d versions, want 2", err, len(vs))
	}
	if err := a.RestoreVersion(ctx, m.ID, "a.txt", "v-nope"); err == nil {
		t.Fatal("RestoreVersion of an unknown version must fail")
	}
	if err := a.RestoreVersion(ctx, m.ID, "a.txt", ""); err == nil {
		t.Fatal("RestoreVersion with an empty version id must fail")
	}
	if _, err := a.ListVersions(ctx, "mnt-nope", "a.txt"); err == nil {
		t.Fatal("ListVersions on an unknown mount must fail")
	}
}

func TestListVersionsUnsupportedMount(t *testing.T) {
	// Local folders have no version history: the app reports
	// "not supported" instead of failing halfway.
	dir := t.TempDir()
	writeLocal(t, dir, "a.txt", "a")
	a := newTestApp(t)
	m := addLocalMount(t, a, dir, "本地")
	ctx := context.Background()
	if _, err := a.ListVersions(ctx, m.ID, "a.txt"); err == nil {
		t.Fatal("ListVersions on a local mount must fail (no Versioner)")
	}
	if err := a.RestoreVersion(ctx, m.ID, "a.txt", "v1"); err == nil {
		t.Fatal("RestoreVersion on a local mount must fail (no Versioner)")
	}
}

// TestExecuteCopyUsesCopierSameMount proves the server-side path:
// OpenRead is faulted for the source (fakeMegaFailRead), so a
// streaming copy would fail — but the same-mount copy succeeds
// because the Copier never reads the file, and copyCalls proves
// it went through Copy, not Upload.
func TestExecuteCopyUsesCopierSameMount(t *testing.T) {
	resetFakeMegaFileOps(t)
	fakeMegaCaps = domain.Caps{CanRename: true, CanDelete: true, DeleteToTrash: true,
		SupportsVersions: true, SupportsServerSideCopy: true}
	a := newTestApp(t)
	registerFakeMega(t)
	startTestWorker(t, a)
	m := addFakeMegaMount(t, a)
	p := fakeMegaProviderOf(t, a, m.ID)
	seedFakeMegaFile(p, "a.txt", "hello")
	fakeMegaFailRead = map[string]bool{"a.txt": true}
	ctx := context.Background()
	res, err := a.BatchCopy(ctx, m.ID, []string{"a.txt"}, m.ID, "copies")
	if err != nil {
		t.Fatalf("BatchCopy: %v", err)
	}
	if len(res.Transfers) != 1 {
		t.Fatalf("Transfers = %d, want 1", len(res.Transfers))
	}
	done := waitTransfersTerminal(t, a, transferIDs(res.Transfers), 10*time.Second)
	if tr := done[res.Transfers[0].ID]; tr.Status != domain.TransferCompleted {
		t.Fatalf("same-mount copy status = %s, want completed (error: %v)", tr.Status, tr.Error)
	}
	if p.copyCalls != 1 {
		t.Fatalf("copyCalls = %d, want 1 (must use Copier, not streaming)", p.copyCalls)
	}
	if n, err := p.Stat(ctx, "copies/a.txt"); err != nil || n.Size != 5 {
		t.Fatalf("copied file: %+v %v", n, err)
	}
	if _, err := p.Stat(ctx, "a.txt"); err != nil {
		t.Fatalf("source must survive a copy: %v", err)
	}
}

// TestExecuteCopyFallsBackWithoutCopier: the local driver has no
// Copier, so a same-mount copy still works — through the streaming
// OpenRead -> Upload fallback, not the server-side path.
func TestExecuteCopyFallsBackWithoutCopier(t *testing.T) {
	dir := t.TempDir()
	writeLocal(t, dir, "a.txt", "hello")
	a := newTestApp(t)
	startTestWorker(t, a)
	m := addLocalMount(t, a, dir, "本地")
	ctx := context.Background()
	res, err := a.BatchCopy(ctx, m.ID, []string{"a.txt"}, m.ID, "copies")
	if err != nil {
		t.Fatalf("BatchCopy: %v", err)
	}
	if len(res.Transfers) != 1 {
		t.Fatalf("Transfers = %d, want 1", len(res.Transfers))
	}
	done := waitTransfersTerminal(t, a, transferIDs(res.Transfers), 10*time.Second)
	if tr := done[res.Transfers[0].ID]; tr.Status != domain.TransferCompleted {
		t.Fatalf("same-mount local copy status = %s, want completed (error: %v)", tr.Status, tr.Error)
	}
	data, err := os.ReadFile(filepath.Join(dir, "copies", "a.txt"))
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("copied content: %q, want %q", data, "hello")
	}
}
