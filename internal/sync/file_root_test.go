// SPDX-License-Identifier: MPL-2.0
package sync

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// fileTreeProvider is an in-memory tree Provider for file-root tests.
// Stat distinguishes files from folders; List returns direct
// children; List on a file ref returns empty with nil error — the
// S3 behaviour that used to make a file root scan to nothing
// (S3 appends "/" to a file ref and matches no objects).
type fileTreeEntry struct {
	isDir bool
	data  []byte
	mtime time.Time
}

type fileTreeProvider struct {
	mu    sync.Mutex
	nodes map[string]*fileTreeEntry // keys like "/docs/report.pdf"; "/" is the root dir
}

func fileTreeKey(ref string) string {
	ref = strings.Trim(ref, "/")
	if ref == "" {
		return "/"
	}
	return "/" + ref
}

func newFileTreeProvider(entries map[string]*fileTreeEntry) *fileTreeProvider {
	if entries == nil {
		entries = map[string]*fileTreeEntry{}
	}
	return &fileTreeProvider{nodes: entries}
}

func (p *fileTreeProvider) nodeFor(k string, e *fileTreeEntry) domain.Node {
	size := int64(len(e.data))
	if e.isDir {
		size = -1
	}
	name := path.Base(k)
	if k == "/" {
		name = "/"
	}
	return domain.Node{Path: k, Ref: k, Name: name, IsDir: e.isDir, Size: size, ModTime: e.mtime}
}

func (p *fileTreeProvider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if fileTreeKey(ref) == "/" {
		return domain.Node{Path: "/", Ref: "/", Name: "/", IsDir: true, Size: -1}, nil
	}
	e, ok := p.nodes[fileTreeKey(ref)]
	if !ok {
		return domain.Node{}, fmt.Errorf("tree: no such node %s", ref)
	}
	return p.nodeFor(fileTreeKey(ref), e), nil
}

func (p *fileTreeProvider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// A file has no children — like S3, whose List appends "/" to
	// a file ref and matches nothing: empty, nil error.
	if e, ok := p.nodes[fileTreeKey(ref)]; ok && !e.isDir {
		return nil, nil
	}
	var out []domain.Node
	for k, e := range p.nodes {
		if k == "/" || path.Dir(k) != fileTreeKey(ref) {
			continue
		}
		out = append(out, p.nodeFor(k, e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (p *fileTreeProvider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.nodes[fileTreeKey(ref)]
	if !ok || e.isDir {
		return nil, fmt.Errorf("tree: no such file %s", ref)
	}
	return io.NopCloser(bytes.NewReader(e.data)), nil
}

func (p *fileTreeProvider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return domain.Node{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	k := fileTreeKey(ref)
	// Implicit parent dirs, like every cloud driver.
	for d := path.Dir(k); d != "/" && d != "."; d = path.Dir(d) {
		if _, ok := p.nodes[d]; !ok {
			p.nodes[d] = &fileTreeEntry{isDir: true}
		}
	}
	p.nodes[k] = &fileTreeEntry{data: b, mtime: time.Now().UTC()}
	return p.nodeFor(k, p.nodes[k]), nil
}

func (p *fileTreeProvider) Mkdir(ctx context.Context, ref string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nodes[fileTreeKey(ref)] = &fileTreeEntry{isDir: true}
	return nil
}

func (p *fileTreeProvider) Rename(ctx context.Context, src, dst string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	sk, dk := fileTreeKey(src), fileTreeKey(dst)
	e, ok := p.nodes[sk]
	if !ok {
		return fmt.Errorf("tree: no such node %s", src)
	}
	p.nodes[dk] = e
	delete(p.nodes, sk)
	return nil
}

func (p *fileTreeProvider) Delete(ctx context.Context, ref string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.nodes, fileTreeKey(ref))
	return nil
}

func (p *fileTreeProvider) Caps() domain.Caps { return domain.Caps{} }

var _ storage.Provider = (*fileTreeProvider)(nil)

var fileRootTime = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func fileRootTree() *fileTreeProvider {
	return newFileTreeProvider(map[string]*fileTreeEntry{
		"/docs":            {isDir: true},
		"/docs/report.pdf": {data: []byte("pdf-bytes"), mtime: fileRootTime},
		"/docs/notes.txt":  {data: []byte("notes"), mtime: fileRootTime},
		"/empty":           {isDir: true},
	})
}

// TestScanFileRoot: a single file as the sync root seeds the
// snapshot with itself (keyed by basename) instead of scanning to
// an empty snapshot the way List-on-a-file-ref does. Folder roots
// are unchanged, and an empty folder / missing ref still scan to
// an empty snapshot with nil error.
func TestScanFileRoot(t *testing.T) {
	p := fileRootTree()
	ctx := t.Context()

	snap, err := Scan(ctx, p, "/docs/report.pdf")
	if err != nil {
		t.Fatalf("Scan file root: %v", err)
	}
	if len(snap.Nodes) != 1 {
		t.Fatalf("file root snapshot has %d nodes, want 1: %v", len(snap.Nodes), snap.Nodes)
	}
	n, ok := snap.Nodes["/report.pdf"]
	if !ok {
		t.Fatalf("file root snapshot keys %v, want [/report.pdf]", keysOf(snap))
	}
	if n.IsDir || n.Size != 9 {
		t.Fatalf("seeded node %+v, want a 9-byte file", n)
	}

	snap, err = Scan(ctx, p, "/docs")
	if err != nil {
		t.Fatalf("Scan folder root: %v", err)
	}
	if len(snap.Nodes) != 2 {
		t.Fatalf("folder root snapshot has %d nodes, want 2 (report.pdf + notes.txt)", len(snap.Nodes))
	}
	if _, ok := snap.Nodes["/report.pdf"]; !ok {
		t.Fatalf("folder scan lost /report.pdf: %v", keysOf(snap))
	}

	snap, err = Scan(ctx, p, "/empty")
	if err != nil {
		t.Fatalf("Scan empty folder must not error: %v", err)
	}
	if len(snap.Nodes) != 0 {
		t.Fatalf("empty folder snapshot has %d nodes, want 0", len(snap.Nodes))
	}

	snap, err = Scan(ctx, p, "/missing")
	if err != nil {
		t.Fatalf("Scan missing ref must not error (Stat falls back to the List walk): %v", err)
	}
	if len(snap.Nodes) != 0 {
		t.Fatalf("missing ref snapshot has %d nodes, want 0", len(snap.Nodes))
	}
}

// TestScanFileRootPlan: the full scan → compare → plan pipeline
// for a file root. Against an empty target the plan has exactly
// one upload of the file; against an identical file it has zero
// items; against a different-sized file it has one update.
func TestScanFileRootPlan(t *testing.T) {
	ctx := t.Context()
	src := fileRootTree()

	dstEmpty := newFileTreeProvider(map[string]*fileTreeEntry{})
	srcSnap, err := Scan(ctx, src, "/docs/report.pdf")
	if err != nil {
		t.Fatalf("Scan src: %v", err)
	}
	dstSnap, err := Scan(ctx, dstEmpty, "/")
	if err != nil {
		t.Fatalf("Scan dst: %v", err)
	}
	plan := BuildPlan("j1", domain.DirectionCopy, Compare(srcSnap, dstSnap, domain.CompareSizeTime, nil))
	if len(plan.Items) != 1 || plan.Items[0].Path != "/report.pdf" || plan.Items[0].Action != domain.ActionUpload {
		t.Fatalf("plan vs empty target = %+v, want one upload of /report.pdf", plan.Items)
	}

	dstSame := newFileTreeProvider(map[string]*fileTreeEntry{
		"/report.pdf": {data: []byte("pdf-bytes"), mtime: fileRootTime},
	})
	dstSnap, err = Scan(ctx, dstSame, "/")
	if err != nil {
		t.Fatalf("Scan dst same: %v", err)
	}
	plan = BuildPlan("j1", domain.DirectionCopy, Compare(srcSnap, dstSnap, domain.CompareSizeTime, nil))
	if len(plan.Items) != 0 {
		t.Fatalf("plan vs identical file = %+v, want 0 items", plan.Items)
	}

	dstDiff := newFileTreeProvider(map[string]*fileTreeEntry{
		"/report.pdf": {data: []byte("old"), mtime: fileRootTime},
	})
	dstSnap, err = Scan(ctx, dstDiff, "/")
	if err != nil {
		t.Fatalf("Scan dst diff: %v", err)
	}
	plan = BuildPlan("j1", domain.DirectionCopy, Compare(srcSnap, dstSnap, domain.CompareSizeTime, nil))
	if len(plan.Items) != 1 || plan.Items[0].Action != domain.ActionUpdate {
		t.Fatalf("plan vs different-sized file = %+v, want one update", plan.Items)
	}
}

// TestRootedProviderFileRoot: the execution wrapper for a file
// root resolves the plan item to the file itself — a plain
// PrefixedProvider would join root + basename
// ("/docs/report.pdf/report.pdf") and read a path that does not
// exist. The target side of a file-to-file job maps the item to
// the target file ref.
func TestRootedProviderFileRoot(t *testing.T) {
	ctx := t.Context()
	src := fileRootTree()
	wrapped := RootedProvider(ctx, src, "/docs/report.pdf")
	rc, err := wrapped.OpenRead(ctx, "/report.pdf")
	if err != nil {
		t.Fatalf("OpenRead through file-root wrapper: %v (a double-joined path would fail here)", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "pdf-bytes" {
		t.Fatalf("OpenRead through file-root wrapper = %q, want %q", got, "pdf-bytes")
	}

	dst := newFileTreeProvider(map[string]*fileTreeEntry{
		"/old-name.pdf": {data: []byte("old"), mtime: fileRootTime},
	})
	target := RootedProvider(ctx, dst, "/old-name.pdf")
	if _, err := target.Upload(ctx, "/report.pdf", strings.NewReader("pdf-bytes"), 9); err != nil {
		t.Fatalf("Upload through target file-root wrapper: %v", err)
	}
	st, err := dst.Stat(ctx, "/old-name.pdf")
	if err != nil {
		t.Fatalf("Stat target file after upload: %v", err)
	}
	if st.Size != 9 {
		t.Fatalf("target file size after upload = %d, want 9", st.Size)
	}
	if _, err := dst.Stat(ctx, "/report.pdf"); err == nil {
		t.Fatal("upload must land on the target file ref (/old-name.pdf), not on /report.pdf")
	}

	// A folder root still gets the plain prefixed wrapper.
	folder := RootedProvider(ctx, src, "/docs")
	rc, err = folder.OpenRead(ctx, "/report.pdf")
	if err != nil {
		t.Fatalf("OpenRead through folder wrapper: %v", err)
	}
	got, _ = io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "pdf-bytes" {
		t.Fatalf("OpenRead through folder wrapper = %q, want %q", got, "pdf-bytes")
	}
}

// TestExecuteFileRootEndToEnd: the whole fixed path — Scan a file
// root, Compare, BuildPlan, Execute with the rooted wrappers —
// lands the file's bytes on the target. This is the run the user
// reported as "完成 0/0/0": before the fix the plan was empty and
// the runner returned success having copied nothing.
func TestExecuteFileRootEndToEnd(t *testing.T) {
	ctx := t.Context()
	src := fileRootTree()
	dst := newFileTreeProvider(map[string]*fileTreeEntry{})

	srcSnap, err := Scan(ctx, src, "/docs/report.pdf")
	if err != nil {
		t.Fatalf("Scan src: %v", err)
	}
	dstSnap, err := Scan(ctx, dst, "/")
	if err != nil {
		t.Fatalf("Scan dst: %v", err)
	}
	plan := BuildPlan("j1", domain.DirectionCopy, Compare(srcSnap, dstSnap, domain.CompareSizeTime, nil))
	if len(plan.Items) != 1 {
		t.Fatalf("plan has %d items, want 1 — the file root must produce work", len(plan.Items))
	}
	r := NewRunner(nil, 2, nil)
	run, err := r.Execute(ctx, plan, RootedProvider(ctx, src, "/docs/report.pdf"), RootedProvider(ctx, dst, "/"), "src", "dst")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v, want completed", run.Status)
	}
	rc, err := dst.OpenRead(ctx, "/report.pdf")
	if err != nil {
		t.Fatalf("OpenRead dst after execute: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "pdf-bytes" {
		t.Fatalf("dst /report.pdf = %q, want %q", got, "pdf-bytes")
	}
}
