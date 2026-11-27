// SPDX-License-Identifier: MPL-2.0
package sync

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// PrefixedProvider wraps a provider so every ref is resolved under
// a job's sync root. Scan keys are relative to the sync root (see
// Scan), so execution must re-attach the root: a plan item "/a.txt"
// for a job whose source root is "/photos" reads
// "/photos/a.txt" from the provider.
//
// root may be "" or "/" (pass-through), a cloud path ("/photos"),
// or a local pseudo-mount ref ("local:///home/u/photos" — the
// scheme is stripped, the absolute path is the prefix).
func PrefixedProvider(p storage.Provider, rootRef string) storage.Provider {
	root := strings.TrimPrefix(rootRef, "local://")
	root = strings.TrimPrefix(root, "local:")
	root = strings.Trim(root, "/")
	if root == "" {
		return p
	}
	return &prefixedProvider{inner: p, root: root}
}

type prefixedProvider struct {
	inner storage.Provider
	root  string
}

func (p *prefixedProvider) join(ref string) string {
	return "/" + p.root + "/" + strings.Trim(ref, "/")
}

func (p *prefixedProvider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	return p.inner.List(ctx, p.join(ref))
}
func (p *prefixedProvider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	return p.inner.Stat(ctx, p.join(ref))
}
func (p *prefixedProvider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	return p.inner.OpenRead(ctx, p.join(ref))
}
func (p *prefixedProvider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	return p.inner.Upload(ctx, p.join(ref), r, size)
}
func (p *prefixedProvider) Mkdir(ctx context.Context, ref string) error {
	return p.inner.Mkdir(ctx, p.join(ref))
}
func (p *prefixedProvider) Rename(ctx context.Context, src, dst string) error {
	return p.inner.Rename(ctx, p.join(src), p.join(dst))
}
func (p *prefixedProvider) Delete(ctx context.Context, ref string) error {
	return p.inner.Delete(ctx, p.join(ref))
}
func (p *prefixedProvider) Caps() domain.Caps { return p.inner.Caps() }

var _ storage.Provider = (*prefixedProvider)(nil)

// FileRootProvider wraps a provider whose sync root is a single
// file. Plan item paths are relative to the sync root; for a file
// root the only meaningful item is the file itself, so every ref
// resolves to the root file. This is the execution counterpart of
// Scan seeding a file root by basename: without it PrefixedProvider
// would join the root and the basename ("/a.iso/a.iso") and read a
// path that does not exist.
func FileRootProvider(p storage.Provider, rootRef string) storage.Provider {
	return &fileRootProvider{inner: p, root: rootRef}
}

type fileRootProvider struct {
	inner storage.Provider
	root  string
}

func (p *fileRootProvider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	n, err := p.inner.Stat(ctx, p.root)
	if err != nil {
		return nil, err
	}
	return []domain.Node{n}, nil
}
func (p *fileRootProvider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	return p.inner.Stat(ctx, p.root)
}
func (p *fileRootProvider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	return p.inner.OpenRead(ctx, p.root)
}
func (p *fileRootProvider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	return p.inner.Upload(ctx, p.root, r, size)
}
func (p *fileRootProvider) Mkdir(ctx context.Context, ref string) error {
	return fmt.Errorf("sync: cannot create a folder under a file sync root")
}
func (p *fileRootProvider) Rename(ctx context.Context, src, dst string) error {
	return fmt.Errorf("sync: cannot rename inside a file sync root")
}
func (p *fileRootProvider) Delete(ctx context.Context, ref string) error {
	return p.inner.Delete(ctx, p.root)
}
func (p *fileRootProvider) Caps() domain.Caps { return p.inner.Caps() }

var _ storage.Provider = (*fileRootProvider)(nil)

// RootedProvider wraps a provider for a job's sync root, picking
// the wrapper that matches what the root actually is: a file root
// gets FileRootProvider (every ref is the file itself), a folder
// root gets PrefixedProvider (refs resolve under the folder). A
// Stat failure — or a zero node that names nothing — falls back to
// PrefixedProvider, the old behaviour, so a root a driver cannot
// Stat still runs exactly as before.
func RootedProvider(ctx context.Context, p storage.Provider, rootRef string) storage.Provider {
	if node, err := p.Stat(ctx, rootRef); err == nil && !node.IsDir && node.Name != "" {
		return FileRootProvider(p, rootRef)
	}
	return PrefixedProvider(p, rootRef)
}
