// SPDX-License-Identifier: MPL-2.0
package local

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckzk/zy-drive/internal/storage"
)

// TestResolveSyncPaths: the sync engine passes display paths
// ("/photos/a.jpg") to providers. For a real mount rooted at a
// folder, that must resolve under the root — not at the OS root.
// An absolute path that genuinely lies under the root is still
// taken literally, and relative refs keep escaping the root.
func TestResolveSyncPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("bbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := New(storage.Config{Extra: map[string]string{"root": root}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Sync-style display path.
	rc, err := p.OpenRead(ctx, "/sub/b.txt")
	if err != nil {
		t.Fatalf("OpenRead(\"/sub/b.txt\"): %v (must resolve under the mount root)", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "bbb" {
		t.Fatalf("OpenRead(\"/sub/b.txt\") = %q, want bbb", got)
	}
	// Genuine absolute path under the root.
	abs := filepath.Join(root, "sub", "b.txt")
	rc, err = p.OpenRead(ctx, abs)
	if err != nil {
		t.Fatalf("OpenRead(%q): %v", abs, err)
	}
	_ = rc.Close()
	// Relative ref still works.
	if _, err := p.Stat(ctx, "sub/b.txt"); err != nil {
		t.Fatalf("Stat(\"sub/b.txt\"): %v", err)
	}
	// A relative ref escaping the root is still rejected.
	if _, err := p.Stat(ctx, "../outside.txt"); err == nil {
		t.Fatal("Stat(\"../outside.txt\") must be rejected")
	}
	// List with a sync-style path.
	nodes, err := p.List(ctx, "/sub")
	if err != nil {
		t.Fatalf("List(\"/sub\"): %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "b.txt" {
		t.Fatalf("List(\"/sub\") = %+v, want [b.txt]", nodes)
	}
	// Upload with a sync-style path lands under the root.
	if _, err := p.Upload(ctx, "/new.txt", strings.NewReader("new"), 3); err != nil {
		t.Fatalf("Upload(\"/new.txt\"): %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err != nil {
		t.Fatalf("uploaded file is not under the root: %v", err)
	}
	if _, err := os.Stat("/new.txt"); err == nil {
		t.Fatal("upload escaped to the OS root")
	}
}
