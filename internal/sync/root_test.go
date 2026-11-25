// SPDX-License-Identifier: MPL-2.0
package sync

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

// treeProvider is a List mock that behaves like the real drivers:
// List returns children whose Path is relative to the MOUNT root,
// not to the listed folder.
type treeProvider struct {
	children map[string][]domain.Node
}

func (t *treeProvider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	ref = strings.TrimPrefix(ref, "local://")
	return t.children[strings.Trim(ref, "/")], nil
}
func (t *treeProvider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	return domain.Node{}, nil
}
func (t *treeProvider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (t *treeProvider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	return domain.Node{}, nil
}
func (t *treeProvider) Mkdir(ctx context.Context, ref string) error { return nil }
func (t *treeProvider) Rename(ctx context.Context, src, dst string) error {
	return nil
}
func (t *treeProvider) Delete(ctx context.Context, ref string) error { return nil }
func (t *treeProvider) Caps() domain.Caps                            { return domain.Caps{} }

// TestScanNestedPaths: a recursive scan must not double the parent
// path (the old code prepended it: "/sub/sub/b.txt"), and a scan
// rooted at a subfolder must return keys relative to that root so
// /photos and /backup compare by file name, not by full path.
func TestScanNestedPaths(t *testing.T) {
	p := &treeProvider{children: map[string][]domain.Node{
		"": {
			{Path: "/a.txt", Ref: "a.txt", Name: "a.txt", Size: 1},
			{Path: "/sub", Ref: "sub", Name: "sub", IsDir: true, Size: -1},
		},
		"sub": {
			{Path: "/sub/b.txt", Ref: "sub/b.txt", Name: "b.txt", Size: 2},
			{Path: "/sub/deep", Ref: "sub/deep", Name: "deep", IsDir: true, Size: -1},
		},
		"sub/deep": {
			{Path: "/sub/deep/c.txt", Ref: "sub/deep/c.txt", Name: "c.txt", Size: 3},
		},
	}}
	snap, err := Scan(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/a.txt", "/sub", "/sub/b.txt", "/sub/deep", "/sub/deep/c.txt"}
	if len(snap.Nodes) != len(want) {
		t.Fatalf("scan keys = %v, want %v", keysOf(snap), want)
	}
	for _, k := range want {
		if _, ok := snap.Nodes[k]; !ok {
			t.Fatalf("scan keys = %v, missing %q", keysOf(snap), k)
		}
	}
	// Rooted at /sub: keys relative to /sub.
	snap, err = Scan(context.Background(), p, "/sub")
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"/b.txt", "/deep", "/deep/c.txt"}
	if len(snap.Nodes) != len(want) {
		t.Fatalf("rooted scan keys = %v, want %v", keysOf(snap), want)
	}
	for _, k := range want {
		if _, ok := snap.Nodes[k]; !ok {
			t.Fatalf("rooted scan keys = %v, missing %q", keysOf(snap), k)
		}
	}
	// Rooted at a local pseudo-mount path: the local:// scheme is
	// stripped before the root is removed.
	snap, err = Scan(context.Background(), p, "local:///sub")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Nodes) != 3 {
		t.Fatalf("local:// rooted scan keys = %v, want 3", keysOf(snap))
	}
}

func keysOf(s *Snapshot) []string {
	out := make([]string, 0, len(s.Nodes))
	for k := range s.Nodes {
		out = append(out, k)
	}
	return out
}

// TestPrefixedProvider: execution re-attaches the sync root; an
// empty root passes the provider through unchanged.
func TestPrefixedProvider(t *testing.T) {
	inner := newMockProvider(map[string][]byte{}, 0)
	wrapped := PrefixedProvider(inner, "/photos")
	if _, err := wrapped.Upload(context.Background(), "/a.txt", strings.NewReader("x"), 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := inner.files["/photos/a.txt"]; !ok {
		t.Fatalf("inner files = %v, want the upload at /photos/a.txt", inner.files)
	}
	rc, err := wrapped.OpenRead(context.Background(), "/a.txt")
	if err != nil {
		t.Fatalf("OpenRead through the wrapper: %v", err)
	}
	_ = rc.Close()
	if err := wrapped.Delete(context.Background(), "/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, ok := inner.files["/photos/a.txt"]; ok {
		t.Fatal("delete through the wrapper did not remove /photos/a.txt")
	}
	if got := PrefixedProvider(inner, ""); got != inner {
		t.Fatal("an empty root must pass the provider through unchanged")
	}
	if got := PrefixedProvider(inner, "/"); got != inner {
		t.Fatal("a \"/\" root must pass the provider through unchanged")
	}
	// A local pseudo-mount root: the scheme is stripped.
	wrapped = PrefixedProvider(inner, "local:///home/u/photos")
	if _, err := wrapped.Upload(context.Background(), "/a.txt", strings.NewReader("x"), 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := inner.files["/home/u/photos/a.txt"]; !ok {
		t.Fatalf("inner files = %v, want the upload at /home/u/photos/a.txt", inner.files)
	}
}
