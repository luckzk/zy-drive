// SPDX-License-Identifier: MPL-2.0
package mount

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestWritebackConflictRename: the remote changed while the
// file was edited locally — the upload must NOT overwrite it.
// The local version lands as a conflict copy instead, and the
// user is told.
func TestWritebackConflictRename(t *testing.T) {
	env := newTestEnv(t, 5)
	p := env.providers["drive-1"]
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	p.putFile("/notes.txt", "v1", mtime)
	ctx := context.Background()
	// Open (download) the file, then edit it locally.
	if _, _, err := env.svc.cache.ensure(ctx, p, nil, "drive-1", "/notes.txt", 2, mtime); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	full := env.svc.cache.pathFor("drive-1", "/notes.txt")
	if err := os.WriteFile(full, []byte("v1-local-edit"), 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := env.svc.cache.markDirty("drive-1", "/notes.txt"); err != nil {
		t.Fatalf("markDirty: %v", err)
	}
	env.svc.uploader.enqueue("drive-1", "/notes.txt")
	// Meanwhile another machine changed the remote file.
	p.putFile("/notes.txt", "v2-remote", time.Now().Truncate(time.Second))
	res := env.svc.FlushDirty(5 * time.Second)
	if res.Remaining != 0 {
		t.Fatalf("flush left %d pending", res.Remaining)
	}
	// The remote original is untouched.
	rc, err := p.OpenRead(ctx, "/notes.txt")
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(data) != "v2-remote" {
		t.Fatalf("remote original = %q, want v2-remote (must not be overwritten)", data)
	}
	// The local version is a conflict copy.
	today := time.Now().Format("2006-01-02")
	want := fmt.Sprintf("/notes (conflict copy from this Mac %s).txt", today)
	if _, err := p.Stat(ctx, want); err != nil {
		t.Fatalf("conflict copy %q should exist: %v", want, err)
	}
	rc, err = p.OpenRead(ctx, want)
	if err != nil {
		t.Fatalf("open conflict copy: %v", err)
	}
	data, _ = io.ReadAll(rc)
	_ = rc.Close()
	if string(data) != "v1-local-edit" {
		t.Fatalf("conflict copy = %q, want the local edit", data)
	}
	// The status card tells the user.
	st := env.statusOf("drive-1")
	if !strings.Contains(st.Notice, "另一台电脑也改了") || !strings.Contains(st.Notice, "conflict copy") {
		t.Fatalf("notice should explain the conflict copy, got %q", st.Notice)
	}
}

// TestExitDirtyState: dirty counts feed the exit dialog, and a
// flush uploads them (exit path: 30s best-effort).
func TestExitDirtyState(t *testing.T) {
	env := newTestEnv(t, 5)
	p := env.providers["drive-1"]
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	p.putFile("/a.txt", "aaa", mtime)
	ctx := context.Background()
	if _, _, err := env.svc.cache.ensure(ctx, p, nil, "drive-1", "/a.txt", 3, mtime); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	full := env.svc.cache.pathFor("drive-1", "/a.txt")
	if err := os.WriteFile(full, []byte("aaa-edited"), 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := env.svc.cache.markDirty("drive-1", "/a.txt"); err != nil {
		t.Fatalf("markDirty: %v", err)
	}
	env.svc.uploader.enqueue("drive-1", "/a.txt")
	st := env.svc.ExitState()
	if st.DirtyCount != 1 || st.DirtyBytes != 10 {
		t.Fatalf("exit state = %+v, want 1 dirty file / 10 bytes", st)
	}
	res := env.svc.FlushDirty(5 * time.Second)
	if res.Remaining != 0 || res.Flushed != 1 {
		t.Fatalf("flush = %+v, want 1 flushed / 0 remaining", res)
	}
	st = env.svc.ExitState()
	if st.DirtyCount != 0 || st.DirtyBytes != 0 {
		t.Fatalf("after flush exit state = %+v, want clean", st)
	}
	// The remote now holds the edit.
	rc, err := p.OpenRead(ctx, "/a.txt")
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(data) != "aaa-edited" {
		t.Fatalf("remote = %q, want the edit", data)
	}
}

// TestFailedUploadExposed: after 3 retries a failed upload
// keeps its dirty bit and its file name shows in the status.
func TestFailedUploadExposed(t *testing.T) {
	env := newTestEnv(t, 5)
	p := env.providers["drive-1"]
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	p.putFile("/b.txt", "bbb", mtime)
	ctx := context.Background()
	if _, _, err := env.svc.cache.ensure(ctx, p, nil, "drive-1", "/b.txt", 3, mtime); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	full := env.svc.cache.pathFor("drive-1", "/b.txt")
	if err := os.WriteFile(full, []byte("bbb-edited"), 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := env.svc.cache.markDirty("drive-1", "/b.txt"); err != nil {
		t.Fatalf("markDirty: %v", err)
	}
	env.svc.uploader.enqueue("drive-1", "/b.txt")
	p.uploadErr = errors.New("fake: network down")
	// Flush mode treats backoff'd retries as due, so all 4
	// attempts run at once; the failure is then final.
	res := env.svc.FlushDirty(10 * time.Second)
	if res.Remaining != 0 {
		t.Fatalf("flush left %d pending", res.Remaining)
	}
	st := env.statusOf("drive-1")
	if len(st.FailedUploads) != 1 || st.FailedUploads[0] != "b.txt" {
		t.Fatalf("failed uploads = %v, want [b.txt]", st.FailedUploads)
	}
	// The dirty bit survives: the user's change is not lost.
	if es := env.svc.ExitState(); es.DirtyCount != 1 {
		t.Fatalf("dirty count after final failure = %d, want 1", es.DirtyCount)
	}
}

// TestNewFileUploadsDirectly: a file created in the mount
// (no remote version) uploads without a Stat compare.
func TestNewFileUploadsDirectly(t *testing.T) {
	env := newTestEnv(t, 5)
	p := env.providers["drive-1"]
	ctx := context.Background()
	if _, err := env.svc.cache.createEmpty("drive-1", "/new.txt"); err != nil {
		t.Fatalf("createEmpty: %v", err)
	}
	full := env.svc.cache.pathFor("drive-1", "/new.txt")
	if err := os.WriteFile(full, []byte("fresh"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := env.svc.cache.markDirty("drive-1", "/new.txt"); err != nil {
		t.Fatalf("markDirty: %v", err)
	}
	env.svc.uploader.enqueue("drive-1", "/new.txt")
	res := env.svc.FlushDirty(5 * time.Second)
	if res.Remaining != 0 || res.Flushed != 1 {
		t.Fatalf("flush = %+v, want 1 flushed", res)
	}
	rc, err := p.OpenRead(ctx, "/new.txt")
	if err != nil {
		t.Fatalf("remote new file should exist: %v", err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(data) != "fresh" {
		t.Fatalf("remote new file = %q, want fresh", data)
	}
}
